package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveAuthToken returns the configured shared service token. When no token
// is configured, it creates a random token in AuthTokenFile; only
// AuthDisabled explicitly opts into unauthenticated development mode.
func (c Config) ResolveAuthToken() (string, error) {
	if token := strings.TrimSpace(c.MCPAuthToken); token != "" {
		return token, nil
	}
	if c.AuthDisabled {
		return "", nil
	}
	if path := strings.TrimSpace(c.AuthTokenFile); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if token := strings.TrimSpace(string(data)); token != "" {
				return token, nil
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("read auth token file: %w", err)
		}
		token, err := newAuthToken()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("create auth token directory: %w", err)
		}
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("write auth token file: %w", err)
		}
		return token, nil
	}
	return newAuthToken()
}

func newAuthToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
