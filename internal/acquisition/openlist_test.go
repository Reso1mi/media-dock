package acquisition

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Reso1mi/media-dock/internal/domain"
)

func TestOpenListDownloaderTransfersToConfiguredDestination(t *testing.T) {
	type requestBody struct {
		URL       string `json:"url"`
		DestDir   string `json:"dst_dir"`
		ValidCode string `json:"valid_code"`
	}

	var paths []string
	var transfer requestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "openlist-token" {
			t.Errorf("Authorization = %q, want raw OpenList token", got)
		}
		if got := r.Method; got != http.MethodPost {
			t.Errorf("method = %q, want POST", got)
		}
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/fs/transfer":
			if got := r.Header.Get("Idempotency-Key"); got != "job-1" {
				t.Errorf("Idempotency-Key = %q, want job-1", got)
			}
			if got := r.Header.Get("X-MediaDock-Job-ID"); got != "job-1" {
				t.Errorf("X-MediaDock-Job-ID = %q, want job-1", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&transfer); err != nil {
				t.Errorf("decode transfer request: %v", err)
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloader(server.URL, "openlist-token", "/夸克网盘/MediaDock", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.Candidate{
		Kind:     domain.CandidateKindCloudShare,
		RawURL:   "https://pan.quark.cn/s/share_123?pwd=ignored",
		Password: "4321",
	}

	handle, err := downloader.Start(context.Background(), "job-1", candidate, "")
	if err != nil {
		t.Fatal(err)
	}
	if !handle.Completed || handle.Ownership != HandleOwnershipManaged || handle.RemoteID != "" {
		t.Fatalf("handle = %#v, want completed managed transfer without remote task ID", handle)
	}
	if handle.TargetReference == nil || handle.TargetReference.Operation != domain.OperationShareTransfer || handle.TargetReference.Path != "/夸克网盘/MediaDock" {
		t.Fatalf("fallback target reference = %#v", handle.TargetReference)
	}
	if want := []string{"/api/fs/transfer"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("request paths = %v, want %v", paths, want)
	}
	if transfer.URL != candidate.RawURL || transfer.DestDir != "/夸克网盘/MediaDock" || transfer.ValidCode != candidate.Password {
		t.Fatalf("transfer request = %#v, want candidate URL, configured destination, and password", transfer)
	}
}

func TestOpenListDownloaderDelegatesDestinationPermissionToTransferAPI(t *testing.T) {
	transferCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/transfer" {
			transferCalled = true
			_, _ = w.Write([]byte(`{"code":403,"message":"destination is not writable"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloader(server.URL, "token", "/Quark/MediaDock", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Start(context.Background(), "job-1", domain.Candidate{
		Kind:   domain.CandidateKindCloudShare,
		RawURL: "https://pan.quark.cn/s/share123",
	}, "")
	if err == nil || errors.Is(err, ErrAcquisitionOutcomeUncertain) {
		t.Fatalf("Start error = %v, want a definitive transfer rejection", err)
	}
	if !transferCalled {
		t.Fatal("transfer endpoint was not called")
	}
}

func TestOpenListDownloaderRejectsPendingOperationWithoutIDAsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/transfer" {
			_, _ = w.Write([]byte(`{"code":200,"data":{"status":"transferring","progress":0.2}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-no-id", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Start(context.Background(), "job-no-id", domain.Candidate{
		Kind:   domain.CandidateKindCloudShare,
		RawURL: "https://pan.quark.cn/s/no-operation-id",
	}, "/Quark/Library")
	if !errors.Is(err, ErrAcquisitionOutcomeUncertain) {
		t.Fatalf("pending transfer without operation ID error = %v, want uncertain", err)
	}
}

func TestOpenListDownloaderMarksTransferFailuresUncertain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		response string
	}{
		{name: "OpenList error code", status: http.StatusOK, response: `{"code":500,"message":"partial transfer failure"}`},
		{name: "HTTP error", status: http.StatusBadGateway, response: `{"code":502,"message":"upstream unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()

			downloader, err := NewOpenListDownloader(server.URL, "token", "/Quark/MediaDock", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = downloader.Start(context.Background(), "job-1", domain.Candidate{
				Kind:   domain.CandidateKindCloudShare,
				RawURL: "https://pan.quark.cn/s/share123",
			}, "")
			if !errors.Is(err, ErrAcquisitionOutcomeUncertain) {
				t.Fatalf("Start error = %v, want ErrAcquisitionOutcomeUncertain", err)
			}
		})
	}
}

func TestSupportedOpenListShareMatchesForkDocumentedLinks(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{url: "https://pan.quark.cn/s/share_123", want: true},
		{url: "https://www.alipan.com/s/share-123", want: true},
		{url: "https://www.aliyundrive.com/s/share123", want: true},
		{url: "https://pan.baidu.com/s/1share123", want: true},
		{url: "https://yun.baidu.com/s/1share123", want: true},
		{url: "https://alipan.com/s/share123", want: false},
		{url: "https://aliyundrive.com/s/share123", want: false},
		{url: "https://example.com/s/share123", want: false},
		{url: "https://pan.quark.cn/share/share123", want: false},
		{url: "https://pan.quark.cn/s/分享123", want: false},
	} {
		if got := supportedOpenListShare(tc.url); got != tc.want {
			t.Errorf("supportedOpenListShare(%q) = %t, want %t", tc.url, got, tc.want)
		}
	}
}

func TestOpenListDownloaderSupportsAsyncTransferContract(t *testing.T) {
	var transferCalls, statusCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/transfer":
			transferCalls++
			if got := r.Header.Get("Idempotency-Key"); got != "job-async" {
				t.Errorf("Idempotency-Key = %q, want job-async", got)
			}
			if got := r.Header.Get("X-MediaDock-Job-ID"); got != "job-async" {
				t.Errorf("X-MediaDock-Job-ID = %q, want job-async", got)
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"op-async","status":"transferring","progress":0.25,"message":"copying"}}`))
		case "/api/fs/transfer/status":
			statusCalls++
			var request struct {
				OperationID string `json:"operation_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode status request: %v", err)
			}
			if request.OperationID != "op-async" {
				t.Errorf("status operation_id = %q, want op-async", request.OperationID)
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"op-async","status":"completed","progress":1,"files":[{"path":"/Quark/MediaDock/movie.mkv","name":"movie.mkv","size_bytes":123,"etag":"etag-1"},{"path":"/Quark/Other/escape.mkv","name":"escape.mkv"},{"path":"/Quark/MediaDock/../Other/escape-2.mkv","name":"escape-2.mkv"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloader(server.URL, "token", "/Quark/MediaDock", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.Candidate{
		Kind:   domain.CandidateKindCloudShare,
		RawURL: "https://pan.quark.cn/s/async-share",
	}
	handle, err := downloader.Start(context.Background(), "job-async", candidate, "")
	if err != nil {
		t.Fatalf("start async transfer: %v", err)
	}
	if handle.Completed || handle.RemoteID != "op-async" || handle.TargetReference == nil {
		t.Fatalf("pending handle = %#v, want operation id and incomplete state", handle)
	}
	if handle.TargetReference.OperationID != "op-async" || handle.TargetReference.Path != "/Quark/MediaDock" {
		t.Fatalf("pending target reference = %#v", handle.TargetReference)
	}

	status, err := downloader.Status(context.Background(), handle.RemoteID)
	if err != nil {
		t.Fatalf("read async transfer status: %v", err)
	}
	if status.Status != "transferred" || status.Progress != 1 || status.TargetReference == nil {
		t.Fatalf("completed status = %#v, want transferred with reference", status)
	}
	if len(status.TargetReference.Files) != 1 || status.TargetReference.Files[0].Path != "/Quark/MediaDock/movie.mkv" {
		t.Fatalf("completed file references = %#v, want only configured-destination file", status.TargetReference.Files)
	}
	if transferCalls != 1 || statusCalls != 1 {
		t.Fatalf("transfer calls = %d, status calls = %d, want 1 each", transferCalls, statusCalls)
	}
}

func TestOpenListDownloaderTransferUsesRequestTargetDir(t *testing.T) {
	var transfer struct {
		DestDir string `json:"dst_dir"`
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/api/fs/transfer" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&transfer); err != nil {
			t.Errorf("decode transfer request: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":200,"data":null}`))
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-dynamic", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Start(context.Background(), "job-dynamic", domain.Candidate{
		Kind:   domain.CandidateKindCloudShare,
		RawURL: "https://pan.quark.cn/s/dynamic-share",
	}, "/Quark/Requested")
	if err != nil {
		t.Fatalf("start transfer: %v", err)
	}
	if transfer.DestDir != "/Quark/Requested" {
		t.Fatalf("transfer destination = %q, want /Quark/Requested", transfer.DestDir)
	}
	if !reflect.DeepEqual(paths, []string{"/api/fs/transfer"}) {
		t.Fatalf("request paths = %v, want only transfer endpoint", paths)
	}
}

func TestOpenListStatusFailureDoesNotResubmitTransfer(t *testing.T) {
	transferCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/transfer":
			transferCalls++
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"op-stuck","status":"transferring"}}`))
		case "/api/fs/transfer/status":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":502,"message":"status unavailable"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloader(server.URL, "token", "/Quark/MediaDock", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Start(context.Background(), "job-stuck", domain.Candidate{
		Kind:   domain.CandidateKindCloudShare,
		RawURL: "https://pan.quark.cn/s/stuck-share",
	}, "")
	if err != nil {
		t.Fatalf("start transfer: %v", err)
	}
	if _, err := downloader.Status(context.Background(), "op-stuck"); err == nil {
		t.Fatal("status unexpectedly succeeded")
	}
	if transferCalls != 1 {
		t.Fatalf("transfer calls = %d, want exactly one after status failure", transferCalls)
	}
}

func TestOpenListStartCopy(t *testing.T) {
	type copyRequest struct {
		SourceDir string   `json:"src_dir"`
		TargetDir string   `json:"dst_dir"`
		Names     []string `json:"names"`
		Overwrite bool     `json:"overwrite"`
		Skip      bool     `json:"skip_existing"`
		Merge     bool     `json:"merge"`
	}
	var request copyRequest
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/api/fs/copy" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Idempotency-Key") != "copy-job-1" || r.Header.Get("X-MediaDock-Job-ID") != "copy-job-1" {
			t.Errorf("copy correlation headers are missing: Idempotency-Key=%q X-MediaDock-Job-ID=%q", r.Header.Get("Idempotency-Key"), r.Header.Get("X-MediaDock-Job-ID"))
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode copy request: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":null}`))
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-copy", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := downloader.StartCopy(context.Background(), CopyRequest{
		JobID:         "copy-job-1",
		TargetProfile: "openlist-copy",
		SourcePath:    "/Quark/Incoming/movie.mkv",
		TargetDir:     "/Quark/Library",
		Options:       domain.CopyOptions{Overwrite: true, Merge: true},
	})
	if err != nil {
		t.Fatalf("start OpenList COPY: %v", err)
	}
	if !handle.Completed || handle.RemoteID != "" || handle.RemoteOperation != domain.OperationOpenListCopy {
		t.Fatalf("COPY handle = %#v, want completed synchronous COPY", handle)
	}
	if handle.TargetReference == nil || handle.TargetReference.Operation != domain.OperationOpenListCopy || handle.TargetReference.Path != "/Quark/Library" {
		t.Fatalf("COPY target reference = %#v", handle.TargetReference)
	}
	if !reflect.DeepEqual(paths, []string{"/api/fs/copy"}) {
		t.Fatalf("COPY request paths = %v, want only copy endpoint", paths)
	}
	if request.SourceDir != "/Quark/Incoming" || request.TargetDir != "/Quark/Library" || !reflect.DeepEqual(request.Names, []string{"movie.mkv"}) || !request.Overwrite || request.Skip || !request.Merge {
		t.Fatalf("COPY request = %#v", request)
	}
}

func TestOpenListCopyStatusAndCancel(t *testing.T) {
	var statusIDs []string
	var cancelID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/copy":
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"copy-op-1","status":"copying","progress":0.4}}`))
		case "/api/fs/copy/status":
			var request struct {
				OperationID string `json:"operation_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode COPY status request: %v", err)
			}
			statusIDs = append(statusIDs, request.OperationID)
			_, _ = w.Write([]byte(`{"code":200,"data":{"operation_id":"copy-op-1","status":"completed","progress":1,"files":[{"path":"/Quark/Library/movie.mkv","size_bytes":10},{"path":"/Other/escape.mkv"}]}}`))
		case "/api/fs/copy/cancel":
			var request struct {
				OperationID string `json:"operation_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode COPY cancel request: %v", err)
			}
			cancelID = request.OperationID
			_, _ = w.Write([]byte(`{"code":200,"message":"cancelled","data":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-copy", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := downloader.StartCopy(context.Background(), CopyRequest{
		JobID:      "copy-job-2",
		SourcePath: "/Quark/Incoming/movie.mkv",
		TargetDir:  "/Quark/Library",
	})
	if err != nil {
		t.Fatalf("start asynchronous COPY: %v", err)
	}
	if handle.Completed || handle.RemoteID != "copy-op-1" || handle.TargetReference == nil || handle.TargetReference.Operation != domain.OperationOpenListCopy {
		t.Fatalf("pending COPY handle = %#v", handle)
	}
	status, err := downloader.StatusRequest(context.Background(), RemoteStatusRequest{
		RemoteID:  handle.RemoteID,
		Operation: domain.OperationOpenListCopy,
		TargetDir: "/Quark/Library",
	})
	if err != nil {
		t.Fatalf("read COPY status: %v", err)
	}
	if status.Status != "completed" || status.Progress != 1 || status.TargetReference == nil || len(status.TargetReference.Files) != 1 || status.TargetReference.Files[0].Path != "/Quark/Library/movie.mkv" {
		t.Fatalf("COPY status = %#v", status)
	}
	if err := downloader.CancelRequest(context.Background(), RemoteCancelRequest{RemoteID: handle.RemoteID, Operation: domain.OperationOpenListCopy}); err != nil {
		t.Fatalf("cancel COPY: %v", err)
	}
	if !reflect.DeepEqual(statusIDs, []string{"copy-op-1"}) || cancelID != "copy-op-1" {
		t.Fatalf("status IDs = %v, cancel ID = %q", statusIDs, cancelID)
	}
}

func TestOpenListCopyRejectsInvalidPathsAndOptions(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":200,"data":null}`))
	}))
	defer server.Close()
	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-copy", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		source string
		target string
		opts   domain.CopyOptions
	}{
		{name: "relative source", source: "Quark/movie.mkv", target: "/Quark/Library"},
		{name: "relative target", source: "/Quark/movie.mkv", target: "Quark/Library"},
		{name: "parent segment", source: "/Quark/../movie.mkv", target: "/Quark/Library"},
		{name: "backslash", source: `/Quark\\movie.mkv`, target: "/Quark/Library"},
		{name: "root source", source: "/", target: "/Quark/Library"},
		{name: "directory source", source: "/Quark/Incoming/", target: "/Quark/Library"},
		{name: "same file", source: "/Quark/Library/movie.mkv", target: "/Quark/Library"},
		{name: "conflicting options", source: "/Quark/movie.mkv", target: "/Quark/Library", opts: domain.CopyOptions{Overwrite: true, SkipExisting: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := downloader.StartCopy(context.Background(), CopyRequest{JobID: "invalid", SourcePath: test.source, TargetDir: test.target, Options: test.opts})
			if err == nil {
				t.Fatal("COPY unexpectedly accepted invalid input")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid COPY requests reached OpenList %d times", calls)
	}
}

func TestOpenListUsesNativeCopyTaskAPI(t *testing.T) {
	var paths []string
	var infoID, cancelID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/fs/copy":
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"message":"Successfully created 1 copy task(s)","tasks":[{"id":"native-copy-1","state":0,"status":"uploading","progress":25}]}}`))
		case "/api/task/copy/info":
			if r.Method != http.MethodPost {
				t.Errorf("native task info method = %q, want POST", r.Method)
			}
			infoID = r.URL.Query().Get("tid")
			_, _ = w.Write([]byte(`{"code":200,"data":{"id":"native-copy-1","state":2,"status":"uploading","progress":100}}`))
		case "/api/task/copy/cancel":
			if r.Method != http.MethodPost {
				t.Errorf("native task cancel method = %q, want POST", r.Method)
			}
			cancelID = r.URL.Query().Get("tid")
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":null}`))
		default:
			t.Errorf("unexpected OpenList path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-native", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := downloader.StartCopy(context.Background(), CopyRequest{
		JobID:         "native-copy-job",
		TargetProfile: "openlist-native",
		SourcePath:    "/Quark/Incoming/movie.mkv",
		TargetDir:     "/Quark/Library",
		Options:       domain.CopyOptions{SkipExisting: true},
	})
	if err != nil {
		t.Fatalf("start native OpenList COPY: %v", err)
	}
	if handle.Completed || handle.RemoteID != "native-copy-1" {
		t.Fatalf("native COPY handle = %#v, want pending native task", handle)
	}

	status, err := downloader.StatusRequest(context.Background(), RemoteStatusRequest{
		RemoteID:  handle.RemoteID,
		Operation: domain.OperationOpenListCopy,
		TargetDir: "/Quark/Library",
	})
	if err != nil {
		t.Fatalf("read native COPY status: %v", err)
	}
	if status.Status != "completed" || status.Progress != 1 || status.TargetReference == nil || status.TargetReference.Path != "/Quark/Library" {
		t.Fatalf("native COPY status = %#v, want completed at target directory", status)
	}
	if err := downloader.CancelRequest(context.Background(), RemoteCancelRequest{RemoteID: handle.RemoteID, Operation: domain.OperationOpenListCopy}); err != nil {
		t.Fatalf("cancel native COPY: %v", err)
	}
	if infoID != "native-copy-1" || cancelID != "native-copy-1" {
		t.Fatalf("native task IDs: info=%q cancel=%q", infoID, cancelID)
	}
	if !reflect.DeepEqual(paths, []string{"/api/fs/copy", "/api/task/copy/info", "/api/task/copy/cancel"}) {
		t.Fatalf("native COPY request paths = %v", paths)
	}
}

func TestOpenListTreatsNativeSynchronousCopyAsCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/copy" {
			t.Errorf("unexpected synchronous COPY path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"message":"Copy operations completed immediately"}}`))
	}))
	defer server.Close()

	downloader, err := NewOpenListDownloaderForProfile(server.URL, "token", "openlist-native", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := downloader.StartCopy(context.Background(), CopyRequest{
		JobID:      "native-sync-copy",
		SourcePath: "/Quark/Incoming/movie.mkv",
		TargetDir:  "/Quark/Library",
	})
	if err != nil {
		t.Fatalf("start synchronous OpenList COPY: %v", err)
	}
	if !handle.Completed || handle.RemoteID != "" {
		t.Fatalf("synchronous COPY handle = %#v, want completed without task ID", handle)
	}
}
