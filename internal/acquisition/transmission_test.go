package acquisition

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Reso1mi/media-dock/internal/domain"
)

func TestTransmissionNegotiatesSessionAndAddsTorrent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			w.Header().Set("X-Transmission-Session-Id", "session-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "session-1" {
			t.Fatalf("session id was not sent on retry")
		}
		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Method != "torrent-add" || request.Arguments["filename"] != "magnet:?xt=urn:btih:test" {
			t.Fatalf("unexpected RPC request: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-added":{"id":7,"hashString":"ABC","name":"demo"}}}`))
	}))
	defer server.Close()

	downloader := NewTransmissionDownloader(server.URL, "user", "pass", server.Client())
	handle, err := downloader.Start(context.Background(), "job-1", domain.Candidate{
		Kind:   "magnet",
		RawURL: "magnet:?xt=urn:btih:test",
	}, "/downloads/incoming/job-1")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if handle.RemoteID != "ABC" || calls.Load() != 2 {
		t.Fatalf("unexpected handle or call count: %#v calls=%d", handle, calls.Load())
	}
}

func TestTransmissionDoesNotAcceptCloudCandidate(t *testing.T) {
	downloader := NewTransmissionDownloader("http://unused", "", "", nil)
	if downloader.Supports(domain.Candidate{Kind: "cloud", RawURL: "https://115.example/share"}) {
		t.Fatal("Transmission must not claim cloud-drive shares")
	}
}
