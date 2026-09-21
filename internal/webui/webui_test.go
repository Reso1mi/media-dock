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
	body := recorder.Body.String()
	if !strings.Contains(body, "MediaDock") {
		t.Fatal("index page does not contain the application name")
	}
	for _, fragment := range []string{
		`<p class="eyebrow">运营概览</p>`,
		`<h1>组件状态</h1>`,
		`<input id="query" name="query" type="search"`,
		`<input id="token-input" type="password"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("index page is missing valid markup %q", fragment)
		}
	}
	for _, fragment := range []string{
		"<</div>",
		"</span> type=",
		"<label</span>",
		"</h1>组件",
	} {
		if strings.Contains(body, fragment) {
			t.Errorf("index page contains malformed markup %q", fragment)
		}
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
