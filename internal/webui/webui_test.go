package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedUI(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "MediaDock") {
		t.Fatal("index page does not contain the application name")
	}
}

func TestHandlerServesAssetsAndRejectsWrites(t *testing.T) {
	assetRecorder := httptest.NewRecorder()
	assetRequest := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	Handler().ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want %d", assetRecorder.Code, http.StatusOK)
	}

	writeRecorder := httptest.NewRecorder()
	writeRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	Handler().ServeHTTP(writeRecorder, writeRequest)
	if writeRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write status = %d, want %d", writeRecorder.Code, http.StatusMethodNotAllowed)
	}
}
