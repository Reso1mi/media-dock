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

	"nas-bot/internal/acquisition"
	"nas-bot/internal/config"
	"nas-bot/internal/httpapi"
	"nas-bot/internal/search"
	"nas-bot/internal/store"
)

func main() {
	cfg := config.FromEnv()
	logger := log.New(os.Stdout, "media-scout ", log.LstdFlags|log.Lmicroseconds)

	memoryStore := store.NewMemoryStore()
	providers := make([]search.Provider, 0, 2)
	if cfg.PansouBaseURL != "" {
		providers = append(providers, search.NewPansouProvider(cfg.PansouBaseURL, nil))
	}
	if cfg.ProwlarrBaseURL != "" {
		providers = append(providers, search.NewProwlarrProvider(cfg.ProwlarrBaseURL, cfg.ProwlarrAPIKey, nil))
	}
	searchService := search.NewService(memoryStore, providers, cfg.SearchTimeout, cfg.SearchTTL)

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
		default:
			logger.Printf("unknown downloader %q; it will be ignored", name)
		}
	}
	acquisitionService := acquisition.NewService(memoryStore, downloaders, cfg.IncomingDir)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewServer(searchService, acquisitionService, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Printf("listening on %s; search providers=%v; downloaders=%v", cfg.HTTPAddr, searchService.ProviderNames(), acquisitionService.DownloaderNames())
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
}
