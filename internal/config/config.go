package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr     string
	MCPEnabled   bool
	MCPPath      string
	MCPAuthToken string

	PansouBaseURL   string
	ProwlarrBaseURL string
	ProwlarrAPIKey  string
	Downloaders     []string

	TransmissionRPCURL   string
	TransmissionUser     string
	TransmissionPassword string

	IncomingDir   string
	SearchTimeout time.Duration
	SearchTTL     time.Duration
}

func FromEnv() Config {
	return Config{
		HTTPAddr:             env("HTTP_ADDR", ":8080"),
		MCPEnabled:           boolEnv("MCP_ENABLED", true),
		MCPPath:              normalizedPath(env("MCP_PATH", "/mcp")),
		MCPAuthToken:         strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN")),
		PansouBaseURL:        env("PANSOU_BASE_URL", ""),
		ProwlarrBaseURL:      env("PROWLARR_BASE_URL", ""),
		ProwlarrAPIKey:       env("PROWLARR_API_KEY", ""),
		Downloaders:          listEnv("DOWNLOADERS"),
		TransmissionRPCURL:   env("TRANSMISSION_RPC_URL", "http://127.0.0.1:9091/transmission/rpc"),
		TransmissionUser:     env("TRANSMISSION_USER", ""),
		TransmissionPassword: env("TRANSMISSION_PASSWORD", ""),
		IncomingDir:          env("DOWNLOAD_INCOMING_DIR", "./data/downloads/incoming"),
		SearchTimeout:        durationEnv("SEARCH_TIMEOUT", 30*time.Second),
		SearchTTL:            durationEnv("SEARCH_SESSION_TTL", 30*time.Minute),
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
