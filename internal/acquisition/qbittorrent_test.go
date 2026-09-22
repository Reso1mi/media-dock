package acquisition

import (
	"context"
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Reso1mi/media-dock/internal/domain"
)

const testMagnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=demo"

func TestQBittorrentAddsTracksAndCancelsMagnet(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	added := false
	cancelled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil || r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
				t.Fatalf("unexpected login form: %v", r.Form)
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-session", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("hashes") != hash {
				t.Fatalf("unexpected hash query: %q", r.URL.Query().Get("hashes"))
			}
			w.Header().Set("Content-Type", "application/json")
			if !added || cancelled {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"hash":"0123456789abcdef0123456789abcdef01234567","name":"demo","state":"uploading","progress":1}]`))
		case "/api/v2/torrents/add":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse add form: %v", err)
			}
			if r.Form.Get("urls") != testMagnet {
				t.Fatalf("unexpected magnet: %q", r.Form.Get("urls"))
			}
			added = true
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse delete form: %v", err)
			}
			if r.Form.Get("hashes") != hash || r.Form.Get("deleteFiles") != "false" {
				t.Fatalf("unexpected delete form: %v", r.Form)
			}
			cancelled = true
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader := NewQBittorrentDownloader(server.URL, "admin", "secret", server.Client())
	handle, err := downloader.Start(context.Background(), "job-1", domain.Candidate{
		Kind:   domain.CandidateKindMagnet,
		RawURL: testMagnet,
	}, "/downloads/job-1")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if handle.RemoteID != hash || handle.Ownership != HandleOwnershipManaged {
		t.Fatalf("unexpected managed handle: %#v", handle)
	}

	status, err := downloader.Status(context.Background(), hash)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status.Status != "completed" || status.Progress != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if err := downloader.Cancel(context.Background(), hash); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if !cancelled {
		t.Fatal("qBittorrent cancellation was not sent")
	}
}

func TestQBittorrentMarksExistingMagnetAsExternal(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-session", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"hash":"0123456789abcdef0123456789abcdef01234567","name":"existing","state":"downloading","progress":0.4}]`))
		case "/api/v2/torrents/add":
			addCalls++
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader := NewQBittorrentDownloader(server.URL, "admin", "secret", server.Client())
	handle, err := downloader.Start(context.Background(), "job-1", domain.Candidate{
		Kind:   domain.CandidateKindMagnet,
		RawURL: testMagnet,
	}, "")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if handle.RemoteID != hash || handle.Ownership != HandleOwnershipExternal {
		t.Fatalf("unexpected external handle: %#v", handle)
	}
	if addCalls != 0 {
		t.Fatalf("existing qBittorrent task was submitted again: %d", addCalls)
	}
}

func TestQBittorrentOnlyClaimsTrackableMagnets(t *testing.T) {
	downloader := NewQBittorrentDownloader("http://unused", "", "", nil)
	if !downloader.Supports(domain.Candidate{Kind: domain.CandidateKindMagnet, RawURL: testMagnet}) {
		t.Fatal("valid magnet should be supported")
	}
	if downloader.Supports(domain.Candidate{Kind: domain.CandidateKindTorrent, RawURL: "https://example.test/file.torrent"}) {
		t.Fatal("torrent URL should not be claimed without a stable hash")
	}
	if downloader.Supports(domain.Candidate{Kind: domain.CandidateKindMagnet, RawURL: "magnet:?xt=urn:btih:not-a-hash"}) {
		t.Fatal("invalid magnet should not be supported")
	}
}

func TestMagnetInfoHashSupportsBase32(t *testing.T) {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte{0, 17, 34, 51, 68, 85, 102, 119, 136, 153, 170, 187, 204, 221, 238, 255, 16, 32, 48, 64})
	got := magnetInfoHash("magnet:?xt=urn:btih:" + encoded)
	if got != "00112233445566778899aabbccddeeff10203040" {
		t.Fatalf("base32 info hash = %q", got)
	}
}

func TestQBittorrentStructuredAddResponse(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		body     string
		accepted bool
	}{
		{`{"added_torrent_ids":["0123456789abcdef0123456789abcdef01234567"],"failure_count":0,"pending_count":0,"success_count":1}`, true},
		{`{"added_torrent_ids":[],"failure_count":1,"success_count":0}`, false},
		{`{"added_torrent_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"failure_count":0,"success_count":1}`, false},
		{"Ok.", true},
		{"Fails.", false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/info":
					_, _ = w.Write([]byte("[]"))
				case "/api/v2/torrents/add":
					_, _ = w.Write([]byte(tc.body))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			downloader := NewQBittorrentDownloader(server.URL, "admin", "secret", server.Client())
			handle, err := downloader.Start(context.Background(), "job", domain.Candidate{Kind: domain.CandidateKindMagnet, RawURL: testMagnet}, "")
			if (err == nil) != tc.accepted {
				t.Fatalf("Start accepted=%v, want %v (error=%v)", err == nil, tc.accepted, err)
			}
			if tc.accepted && (handle.RemoteID != hash || handle.Ownership != HandleOwnershipManaged) {
				t.Fatalf("unexpected handle: %#v", handle)
			}
		})
	}
}
