package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr      string
	SQLitePath    string
	MCPEnabled    bool
	MCPPath       string
	MCPAuthToken  string
	AuthTokenFile string
	AuthDisabled  bool

	PansouBaseURL   string
	ProwlarrBaseURL string
	ProwlarrAPIKey  string
	Downloaders     []string

	TransmissionRPCURL   string
	TransmissionUser     string
	TransmissionPassword string

	QBittorrentURL      string
	QBittorrentUser     string
	QBittorrentPassword string

	OpenListBaseURL       string
	OpenListAuthToken     string
	OpenListDestDir       string
	OpenListTargetProfile string

	IncomingDir   string
	SearchTimeout time.Duration
	SearchTTL     time.Duration
}

func FromEnv() Config {
	sqlitePath := strings.TrimSpace(os.Getenv("STORAGE_SQLITE_PATH"))
	if sqlitePath == "" {
		sqlitePath = env("SQLITE_PATH", "./data/mediadock.db")
	}
	return Config{
		HTTPAddr:              env("HTTP_ADDR", ":8080"),
		SQLitePath:            sqlitePath,
		MCPEnabled:            boolEnv("MCP_ENABLED", true),
		MCPPath:               normalizedPath(env("MCP_PATH", "/mcp")),
		MCPAuthToken:          firstEnv("AUTH_TOKEN", "MCP_AUTH_TOKEN"),
		AuthTokenFile:         env("AUTH_TOKEN_FILE", "./data/auth-token"),
		AuthDisabled:          boolEnv("AUTH_DISABLED", false),
		PansouBaseURL:         env("PANSOU_BASE_URL", ""),
		ProwlarrBaseURL:       env("PROWLARR_BASE_URL", ""),
		ProwlarrAPIKey:        env("PROWLARR_API_KEY", ""),
		Downloaders:           listEnv("DOWNLOADERS"),
		TransmissionRPCURL:    env("TRANSMISSION_RPC_URL", "http://127.0.0.1:9091/transmission/rpc"),
		TransmissionUser:      env("TRANSMISSION_USER", ""),
		TransmissionPassword:  env("TRANSMISSION_PASSWORD", ""),
		QBittorrentURL:        env("QBITTORRENT_URL", "http://127.0.0.1:8080"),
		QBittorrentUser:       env("QBITTORRENT_USER", "admin"),
		QBittorrentPassword:   env("QBITTORRENT_PASSWORD", ""),
		OpenListBaseURL:       env("OPENLIST_BASE_URL", ""),
		OpenListAuthToken:     env("OPENLIST_AUTH_TOKEN", ""),
		OpenListDestDir:       env("OPENLIST_DEST_DIR", ""),
		OpenListTargetProfile: env("OPENLIST_TARGET_PROFILE", "openlist_default"),
		// Empty means API-only acquisition: MediaDock does not need a local
		// media volume and lets the downloader choose its own remote path.
		IncomingDir:   env("DOWNLOAD_INCOMING_DIR", ""),
		SearchTimeout: durationEnv("SEARCH_TIMEOUT", 30*time.Second),
		SearchTTL:     durationEnv("SEARCH_SESSION_TTL", 30*time.Minute),
	}
}

func normalizedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/mcp"
	}
	value = "/" + strings.Trim(value, "/")
	if value == "/" {
		return "/mcp"
	}
	return value
}

func listEnv(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func boolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
