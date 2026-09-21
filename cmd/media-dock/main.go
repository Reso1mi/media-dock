package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Reso1mi/media-dock/internal/acquisition"
	"github.com/Reso1mi/media-dock/internal/auth"
	"github.com/Reso1mi/media-dock/internal/config"
	"github.com/Reso1mi/media-dock/internal/httpapi"
	"github.com/Reso1mi/media-dock/internal/mcpserver"
	"github.com/Reso1mi/media-dock/internal/search"
	"github.com/Reso1mi/media-dock/internal/store"
	"github.com/Reso1mi/media-dock/internal/webui"
)

func main() {
	cfg := config.FromEnv()
	logger := log.New(os.Stdout, "media-dock ", log.LstdFlags|log.Lmicroseconds)
	authToken, err := cfg.ResolveAuthToken()
	if err != nil {
		logger.Fatalf("resolve auth token: %v", err)
	}

	persistentStore, err := store.OpenSQLite(cfg.SQLitePath)
	if err != nil {
		logger.Fatalf("open sqlite store: %v", err)
	}
	defer persistentStore.Close()

	providers := make([]search.Provider, 0, 2)
	if cfg.PansouBaseURL != "" {
		providers = append(providers, search.NewPansouProvider(cfg.PansouBaseURL, nil))
	}
	if cfg.ProwlarrBaseURL != "" {
		providers = append(providers, search.NewProwlarrProvider(cfg.ProwlarrBaseURL, cfg.ProwlarrAPIKey, nil))
	}
	searchService := search.NewService(persistentStore, providers, cfg.SearchTimeout, cfg.SearchTTL)

	downloaders := make([]acquisition.Downloader, 0, len(cfg.Downloaders))
	for _, name := range cfg.Downloaders {
		switch name {
		case "transmission":
			downloaders = append(downloaders, acquisition.NewTransmissionDownloader(
				cfg.TransmissionRPCURL,
				cfg.TransmissionUser,
				cfg.TransmissionPassword,
				nil,
			))
		case "qbittorrent":
			downloaders = append(downloaders, acquisition.NewQBittorrentDownloader(
				cfg.QBittorrentURL,
				cfg.QBittorrentUser,
				cfg.QBittorrentPassword,
				nil,
			))
		default:
			logger.Printf("unknown downloader %q; it will be ignored", name)
		}
	}
	acquisitionService := acquisition.NewService(persistentStore, downloaders, cfg.IncomingDir)

	rootMux := http.NewServeMux()
	apiServer := httpapi.NewServer(searchService, acquisitionService, logger)
	// Keep liveness checks public, but apply the same bearer policy to every
	// REST business route and the MCP transport. MCP_AUTH_TOKEN is retained as
	// the legacy environment variable name for the shared service token.
	rootMux.Handle("/healthz", apiServer.HealthHandler())
	// The static UI contains no service data and is public so it can present a
	// token prompt. Every REST business route remains protected below.
	rootMux.Handle("/", webui.Handler())
	rootMux.Handle("/api/", auth.BearerAuth(apiServer.Handler(), authToken))
	if cfg.MCPEnabled {
		mcpHandler := mcpserver.NewHTTPHandler(mcpserver.NewServer(searchService, acquisitionService))
		rootMux.Handle(cfg.MCPPath, auth.BearerAuth(mcpHandler, authToken))
		if authToken == "" {
			logger.Printf("warning: authentication is explicitly disabled; REST and MCP business endpoints are unauthenticated")
		}
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           rootMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Do not set WriteTimeout: Streamable HTTP may keep an SSE response open
		// for the lifetime of an MCP session.
		IdleTimeout: 60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	acquisitionService.StartWorker(ctx, acquisition.WorkerOptions{})

	go func() {
		logger.Printf("listening on %s; search providers=%v; downloaders=%v; mcp=%v", cfg.HTTPAddr, searchService.ProviderNames(), acquisitionService.DownloaderNames(), cfg.MCPEnabled)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("HTTP shutdown failed: %v", err)
	}
	if err := acquisitionService.WaitWorker(shutdownCtx); err != nil {
		logger.Printf("worker shutdown failed: %v", err)
	}
}
