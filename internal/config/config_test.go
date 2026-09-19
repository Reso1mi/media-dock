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
