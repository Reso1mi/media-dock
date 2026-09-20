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

func TestTransmissionDoesNotAcceptNonTorrentHTTPOrCloudCandidates(t *testing.T) {
	downloader := NewTransmissionDownloader("http://unused", "", "", nil)
	candidates := []domain.Candidate{
		{Kind: domain.CandidateKindCloudShare, RawURL: "https://115.example/share"},
		{Kind: domain.CandidateKindHTTPFile, RawURL: "https://media.example/video.mp4"},
		{Kind: domain.CandidateKindUnknown, RawURL: "https://media.example/download?id=1"},
	}
	for _, candidate := range candidates {
		if downloader.Supports(candidate) {
			t.Fatalf("Transmission must not claim candidate kind %q", candidate.Kind)
		}
	}
}

func TestTransmissionMarksDuplicateTorrentAsExternal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrent-duplicate":{"id":9,"hashString":"DUPLICATE"}}}`))
	}))
	defer server.Close()

	downloader := NewTransmissionDownloader(server.URL, "", "", server.Client())
	handle, err := downloader.Start(context.Background(), "job-1", domain.Candidate{
		Kind: domain.CandidateKindMagnet, RawURL: "magnet:?xt=urn:btih:duplicate",
	}, "")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if handle.RemoteID != "DUPLICATE" || handle.Ownership != HandleOwnershipExternal {
		t.Fatalf("unexpected duplicate handle: %#v", handle)
	}
}

func TestTransmissionRejectsUnsuccessfulRPCResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"not-found"}`))
	}))
	defer server.Close()

	downloader := NewTransmissionDownloader(server.URL, "", "", server.Client())
	_, err := downloader.Start(context.Background(), "job-1", domain.Candidate{
		Kind:   domain.CandidateKindMagnet,
		RawURL: "magnet:?xt=urn:btih:test",
	}, "/downloads/incoming/job-1")
	if err == nil {
		t.Fatal("unsuccessful Transmission result was accepted")
	}
	if got := err.Error(); got != `transmission RPC torrent-add failed: result "not-found"` {
		t.Fatalf("unexpected error: %q", got)
	}
}
