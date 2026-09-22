package acquisition

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
)

// QBittorrentDownloader talks to qBittorrent's Web API. The first version
// deliberately supports magnet candidates only: a magnet contains the info
// hash needed to reconcile an existing task after a restart, while a generic
// .torrent URL does not return a stable remote ID from qBittorrent's add API.
type QBittorrentDownloader struct {
	InstanceID string
	Endpoint   string
	Username   string
	Password   string
	Client     *http.Client

	mu            sync.Mutex
	authenticated bool
}

func NewQBittorrentDownloader(endpoint, username, password string, client *http.Client) *QBittorrentDownloader {
	return NewNamedQBittorrentDownloader("qbittorrent", endpoint, username, password, client)
}

func NewNamedQBittorrentDownloader(id, endpoint, username, password string, client *http.Client) *QBittorrentDownloader {
	if client == nil {
		jar, _ := cookiejar.New(nil)
		client = &http.Client{Timeout: 30 * time.Second, Jar: jar}
	} else if client.Jar == nil {
		jar, _ := cookiejar.New(nil)
		client.Jar = jar
	}
	return &QBittorrentDownloader{
		InstanceID: strings.TrimSpace(id),
		Endpoint:   strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		Username:   strings.TrimSpace(username),
		Password:   password,
		Client:     client,
	}
}

func (d *QBittorrentDownloader) Name() string {
	if strings.TrimSpace(d.InstanceID) != "" {
		return strings.TrimSpace(d.InstanceID)
	}
	return "qbittorrent"
}

func (d *QBittorrentDownloader) Type() string { return "qbittorrent" }

func (d *QBittorrentDownloader) Supports(candidate domain.Candidate) bool {
	if candidate.Kind != domain.CandidateKindMagnet {
		return false
	}
	return magnetInfoHash(candidate.RawURL) != ""
}

type qbittorrentTorrent struct {
	Hash       string  `json:"hash"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Progress   float64 `json:"progress"`
	TrackerMsg string  `json:"tracker_msg"`
	AmountLeft int64   `json:"amount_left"`
}

func (d *QBittorrentDownloader) ensureAuthenticated(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.authenticated {
		return nil
	}
	return d.loginLocked(ctx)
}

func (d *QBittorrentDownloader) invalidateAuthentication() {
	d.mu.Lock()
	d.authenticated = false
	d.mu.Unlock()
}

func (d *QBittorrentDownloader) loginLocked(ctx context.Context) error {
	if d.Endpoint == "" {
		return fmt.Errorf("qBittorrent URL is not configured")
	}
	form := url.Values{}
	form.Set("username", d.Username)
	form.Set("password", d.Password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create qBittorrent login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.Client.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent login request: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("read qBittorrent login response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qBittorrent login returned HTTP %d", resp.StatusCode)
	}
	if !qBittorrentOK(body) {
		return fmt.Errorf("qBittorrent login failed")
	}
	d.authenticated = true
	return nil
}

func (d *QBittorrentDownloader) request(ctx context.Context, method, path string, query, form url.Values) ([]byte, error) {
	if d.Endpoint == "" {
		return nil, fmt.Errorf("qBittorrent URL is not configured")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := d.ensureAuthenticated(ctx); err != nil {
			return nil, err
		}
		endpoint, err := url.Parse(d.Endpoint + path)
		if err != nil {
			return nil, fmt.Errorf("parse qBittorrent URL: %w", err)
		}
		if query != nil {
			endpoint.RawQuery = query.Encode()
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
		if err != nil {
			return nil, fmt.Errorf("create qBittorrent request: %w", err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := d.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qBittorrent request: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read qBittorrent response: %w", readErr)
		}
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			d.invalidateAuthentication()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("qBittorrent returned HTTP %d", resp.StatusCode)
		}
		return responseBody, nil
	}
	return nil, fmt.Errorf("qBittorrent authentication retry failed")
}

func (d *QBittorrentDownloader) torrents(ctx context.Context, hash string) ([]qbittorrentTorrent, error) {
	query := url.Values{}
	query.Set("hashes", hash)
	body, err := d.request(ctx, http.MethodGet, "/api/v2/torrents/info", query, nil)
	if err != nil {
		return nil, err
	}
	var torrents []qbittorrentTorrent
	if err := json.Unmarshal(body, &torrents); err != nil {
		return nil, fmt.Errorf("decode qBittorrent torrent list: %w", err)
	}
	return torrents, nil
}

func (d *QBittorrentDownloader) Start(ctx context.Context, _ string, candidate domain.Candidate, targetDir string) (Handle, error) {
	if !d.Supports(candidate) {
		return Handle{}, fmt.Errorf("qBittorrent does not support candidate kind %q", candidate.Kind)
	}
	hash := magnetInfoHash(candidate.RawURL)
	existing, err := d.torrents(ctx, hash)
	if err != nil {
		return Handle{}, err
	}
	if len(existing) > 0 {
		return Handle{RemoteID: hash, Ownership: HandleOwnershipExternal}, nil
	}

	form := url.Values{}
	form.Set("urls", candidate.RawURL)
	if strings.TrimSpace(targetDir) != "" {
		form.Set("savepath", targetDir)
		form.Set("autoTMM", "false")
	}
	body, err := d.request(ctx, http.MethodPost, "/api/v2/torrents/add", nil, form)
	if err != nil {
		return Handle{}, err
	}
	if !qBittorrentAddAccepted(body, hash) {
		return Handle{}, fmt.Errorf("qBittorrent rejected torrent: %s", strings.TrimSpace(string(body)))
	}
	return Handle{RemoteID: hash, Ownership: HandleOwnershipManaged}, nil
}

func (d *QBittorrentDownloader) Status(ctx context.Context, remoteID string) (RemoteStatus, error) {
	remoteID = strings.TrimSpace(remoteID)
	if remoteID == "" {
		return RemoteStatus{}, fmt.Errorf("qBittorrent remote id is empty")
	}
	torrents, err := d.torrents(ctx, remoteID)
	if err != nil {
		return RemoteStatus{}, err
	}
	if len(torrents) == 0 {
		return RemoteStatus{}, fmt.Errorf("qBittorrent torrent %q not found", remoteID)
	}
	torrent := torrents[0]
	message := torrent.Name
	if strings.TrimSpace(torrent.TrackerMsg) != "" {
		message = torrent.TrackerMsg
	}
	if torrent.Progress >= 1 {
		return RemoteStatus{Status: "completed", Progress: torrent.Progress, Message: message}, nil
	}
	state := strings.ToLower(strings.TrimSpace(torrent.State))
	switch state {
	case "error", "missingfiles":
		return RemoteStatus{Status: "error", Progress: torrent.Progress, Message: message, Error: message}, nil
	case "uploading", "stalledup", "forcedup", "queuedup", "pausedup":
		return RemoteStatus{Status: "seeding", Progress: torrent.Progress, Message: message}, nil
	case "checkingdl", "checkingup", "checkingresume", "allocating", "moving":
		return RemoteStatus{Status: "checking", Progress: torrent.Progress, Message: message}, nil
	default:
		return RemoteStatus{Status: "downloading", Progress: torrent.Progress, Message: message}, nil
	}
}

func (d *QBittorrentDownloader) Cancel(ctx context.Context, remoteID string) error {
	form := url.Values{}
	form.Set("hashes", strings.TrimSpace(remoteID))
	form.Set("deleteFiles", "false")
	body, err := d.request(ctx, http.MethodPost, "/api/v2/torrents/delete", nil, form)
	if err != nil {
		return err
	}
	if !qBittorrentOK(body) {
		return fmt.Errorf("qBittorrent rejected cancellation: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func qBittorrentOK(body []byte) bool {
	value := strings.TrimSpace(string(body))
	return value == "" || strings.EqualFold(value, "ok.")
}

func qBittorrentAddAccepted(body []byte, hash string) bool {
	if qBittorrentOK(body) {
		return true
	}
	// Recent qBittorrent versions return a structured add result instead of
	// "Ok.". Require this torrent's ID so an unrelated/failed add is not
	// mistaken for success.
	var result struct {
		AddedTorrentIDs []string `json:"added_torrent_ids"`
		FailureCount    int      `json:"failure_count"`
	}
	if json.Unmarshal(body, &result) != nil || result.FailureCount != 0 {
		return false
	}
	for _, addedID := range result.AddedTorrentIDs {
		if strings.EqualFold(addedID, hash) {
			return true
		}
	}
	return false
}

func magnetInfoHash(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return ""
	}
	for _, value := range parsed.Query()["xt"] {
		value = strings.TrimSpace(value)
		const prefix = "urn:btih:"
		if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
			continue
		}
		encoded := value[len(prefix):]
		if len(encoded) == 40 {
			if _, err := hex.DecodeString(encoded); err == nil {
				return strings.ToLower(encoded)
			}
		}
		if len(encoded) == 32 {
			decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded))
			if err == nil && len(decoded) == 20 {
				return hex.EncodeToString(decoded)
			}
		}
	}
	return ""
}
