package acquisition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/Reso1mi/media-dock/internal/domain"
)

type TransmissionDownloader struct {
	InstanceID string
	Endpoint   string
	Username   string
	Password   string
	Client     *http.Client

	mu        sync.Mutex
	sessionID string
}

func NewTransmissionDownloader(endpoint, username, password string, client *http.Client) *TransmissionDownloader {
	return NewNamedTransmissionDownloader("transmission", endpoint, username, password, client)
}

// NewNamedTransmissionDownloader creates a Transmission connection with a
// stable instance ID. The legacy constructor above keeps the original ID for
// single-instance deployments.
func NewNamedTransmissionDownloader(id, endpoint, username, password string, client *http.Client) *TransmissionDownloader {
	if client == nil {
		client = &http.Client{}
	}
	return &TransmissionDownloader{
		InstanceID: strings.TrimSpace(id),
		Endpoint:   strings.TrimSpace(endpoint),
		Username:   username,
		Password:   password,
		Client:     client,
	}
}

func (d *TransmissionDownloader) Name() string {
	if strings.TrimSpace(d.InstanceID) != "" {
		return strings.TrimSpace(d.InstanceID)
	}
	return "transmission"
}

func (d *TransmissionDownloader) Type() string { return "transmission" }

func (d *TransmissionDownloader) Supports(candidate domain.Candidate) bool {
	if strings.TrimSpace(candidate.RawURL) == "" {
		return false
	}
	switch candidate.Kind {
	case domain.CandidateKindMagnet:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate.RawURL)), "magnet:")
	case domain.CandidateKindTorrent:
		// Transmission's torrent-add filename accepts a torrent URL. It does
		// not make a generic HTTP media-file URL a torrent download.
		lower := strings.ToLower(strings.TrimSpace(candidate.RawURL))
		return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
	default:
		return false
	}
}

type rpcRequest struct {
	Method    string         `json:"method"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Tag       int            `json:"tag,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
	Error     *rpcError       `json:"error,omitempty"`
}

func (d *TransmissionDownloader) call(ctx context.Context, method string, arguments map[string]any, result any) error {
	if d.Endpoint == "" {
		return fmt.Errorf("transmission RPC URL is not configured")
	}
	payload, err := json.Marshal(rpcRequest{Method: method, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("encode transmission request: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create transmission request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		d.mu.Lock()
		if d.sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", d.sessionID)
		}
		d.mu.Unlock()
		if d.Username != "" {
			req.SetBasicAuth(d.Username, d.Password)
		}

		resp, err := d.Client.Do(req)
		if err != nil {
			return fmt.Errorf("transmission request: %w", err)
		}
		if resp.StatusCode == http.StatusConflict {
			sessionID := resp.Header.Get("X-Transmission-Session-Id")
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if sessionID == "" {
				return fmt.Errorf("transmission returned 409 without session id")
			}
			d.mu.Lock()
			d.sessionID = sessionID
			d.mu.Unlock()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("transmission returned HTTP %d", resp.StatusCode)
		}
		var decoded rpcResponse
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			return fmt.Errorf("decode transmission response: %w", err)
		}
		if decoded.Error != nil {
			return fmt.Errorf("transmission error %d: %s", decoded.Error.Code, decoded.Error.Message)
		}
		if strings.ToLower(strings.TrimSpace(decoded.Result)) != "success" {
			return fmt.Errorf("transmission RPC %s failed: result %q", method, decoded.Result)
		}
		if result != nil && len(decoded.Arguments) > 0 {
			if err := json.Unmarshal(decoded.Arguments, result); err != nil {
				return fmt.Errorf("decode transmission arguments: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("transmission session negotiation failed")
}

func (d *TransmissionDownloader) Start(ctx context.Context, _ string, candidate domain.Candidate, targetDir string) (Handle, error) {
	if !d.Supports(candidate) {
		return Handle{}, fmt.Errorf("transmission does not support candidate kind %q", candidate.Kind)
	}
	var response struct {
		Added *struct {
			ID         int    `json:"id"`
			HashString string `json:"hashString"`
			Name       string `json:"name"`
		} `json:"torrent-added"`
		Duplicate *struct {
			ID         int    `json:"id"`
			HashString string `json:"hashString"`
			Name       string `json:"name"`
		} `json:"torrent-duplicate"`
	}
	arguments := map[string]any{"filename": candidate.RawURL}
	if strings.TrimSpace(targetDir) != "" {
		arguments["download-dir"] = targetDir
	}
	if err := d.call(ctx, "torrent-add", arguments, &response); err != nil {
		return Handle{}, err
	}
	if response.Added != nil {
		return Handle{
			RemoteID:  transmissionID(response.Added.ID, response.Added.HashString),
			Ownership: HandleOwnershipManaged,
		}, nil
	}
	if response.Duplicate != nil {
		return Handle{
			RemoteID:  transmissionID(response.Duplicate.ID, response.Duplicate.HashString),
			Ownership: HandleOwnershipExternal,
		}, nil
	}
	return Handle{}, fmt.Errorf("transmission did not return a torrent id")
}

func (d *TransmissionDownloader) Status(ctx context.Context, remoteID string) (RemoteStatus, error) {
	var response struct {
		Torrents []struct {
			ID          int     `json:"id"`
			Name        string  `json:"name"`
			Status      int     `json:"status"`
			PercentDone float64 `json:"percentDone"`
			Error       int     `json:"error"`
			ErrorString string  `json:"errorString"`
		} `json:"torrents"`
	}
	if err := d.call(ctx, "torrent-get", map[string]any{
		"ids":    []string{remoteID},
		"fields": []string{"id", "name", "status", "percentDone", "error", "errorString"},
	}, &response); err != nil {
		return RemoteStatus{}, err
	}
	if len(response.Torrents) == 0 {
		return RemoteStatus{}, fmt.Errorf("transmission torrent %q not found", remoteID)
	}
	torrent := response.Torrents[0]
	status := transmissionStatus(torrent.Status, torrent.PercentDone)
	message := torrent.Name
	if torrent.ErrorString != "" {
		message = torrent.ErrorString
	}
	return RemoteStatus{
		Status:   status,
		Progress: torrent.PercentDone,
		Message:  message,
		Error:    torrent.ErrorString,
	}, nil
}

func (d *TransmissionDownloader) Cancel(ctx context.Context, remoteID string) error {
	return d.call(ctx, "torrent-remove", map[string]any{
		"ids":               []string{remoteID},
		"delete-local-data": false,
	}, nil)
}

func transmissionID(id int, hash string) string {
	if strings.TrimSpace(hash) != "" {
		return strings.TrimSpace(hash)
	}
	return strconv.Itoa(id)
}

func transmissionStatus(status int, progress float64) string {
	if progress >= 1 {
		return "completed"
	}
	switch status {
	case 0:
		return "stopped"
	case 1:
		return "checking_wait"
	case 2:
		return "checking"
	case 3:
		return "download_wait"
	case 4:
		return "downloading"
	case 5:
		return "seed_wait"
	case 6:
		return "seeding"
	default:
		return "unknown"
	}
}
