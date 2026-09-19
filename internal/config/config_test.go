package config

import (
	"reflect"
	"testing"
)

func TestFromEnvParsesOptionalDownloaderList(t *testing.T) {
	t.Setenv("DOWNLOADERS", " Transmission, openlist, transmission, ")
	cfg := FromEnv()
	want := []string{"transmission", "openlist"}
	if !reflect.DeepEqual(cfg.Downloaders, want) {
		t.Fatalf("downloaders = %#v, want %#v", cfg.Downloaders, want)
	}
}

func TestFromEnvDefaultsToSearchOnly(t *testing.T) {
	t.Setenv("DOWNLOADERS", "")
	cfg := FromEnv()
	if len(cfg.Downloaders) != 0 {
		t.Fatalf("expected no downloaders by default, got %#v", cfg.Downloaders)
	}
}

func TestFromEnvNormalizesMCPConfiguration(t *testing.T) {
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("MCP_PATH", "//custom/mcp//")
	t.Setenv("MCP_AUTH_TOKEN", "  secret-token  ")

	cfg := FromEnv()
	if cfg.MCPEnabled {
		t.Fatal("MCP should be disabled")
	}
	if cfg.MCPPath != "/custom/mcp" {
		t.Fatalf("MCP path = %q, want /custom/mcp", cfg.MCPPath)
	}
	if cfg.MCPAuthToken != "secret-token" {
		t.Fatalf("MCP auth token was not trimmed: %q", cfg.MCPAuthToken)
	}
}

func TestFromEnvDoesNotAllowMCPAtRoot(t *testing.T) {
	t.Setenv("MCP_PATH", "/")
	if got := FromEnv().MCPPath; got != "/mcp" {
		t.Fatalf("MCP root path = %q, want /mcp", got)
	}
}
