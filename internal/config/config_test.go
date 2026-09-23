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
	if cfg.OpenListTargetProfile != "openlist_default" {
		t.Fatalf("OpenList target profile = %q, want openlist_default", cfg.OpenListTargetProfile)
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

func TestResolveAuthTokenPersistsGeneratedToken(t *testing.T) {
	path := t.TempDir() + "/auth-token"
	cfg := Config{AuthTokenFile: path}
	first, err := cfg.ResolveAuthToken()
	if err != nil {
		t.Fatalf("resolve first token: %v", err)
	}
	second, err := cfg.ResolveAuthToken()
	if err != nil {
		t.Fatalf("resolve second token: %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("tokens are not stable: first=%q second=%q", first, second)
	}
}

func TestResolveAuthTokenRequiresExplicitDisableForEmptyToken(t *testing.T) {
	if token, err := (Config{AuthDisabled: true}).ResolveAuthToken(); err != nil || token != "" {
		t.Fatalf("disabled auth = %q, err=%v", token, err)
	}
	if token, err := (Config{AuthTokenFile: t.TempDir() + "/token"}).ResolveAuthToken(); err != nil || token == "" {
		t.Fatalf("default auth token = %q, err=%v", token, err)
	}
}
