package acquisition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Reso1mi/media-dock/internal/domain"
)

const OpenListRequestTimeout = 6 * time.Minute

var ErrAcquisitionOutcomeUncertain = errors.New("acquisition outcome is uncertain")

// OpenListDownloader is the narrow HTTP adapter used by MediaDock for two
// business operations: share transfer and file COPY. OpenList remains
// responsible for storage permissions and provider-specific behavior; this
// adapter only sends the operation and maps its durable result.
type OpenListDownloader struct {
	baseURL            *url.URL
	authToken          string
	defaultDestination string
	profileID          string
	client             *http.Client
}

func NewOpenListDownloader(baseURL, authToken, destination string, client *http.Client) (*OpenListDownloader, error) {
	return NewOpenListDownloaderWithProfile(baseURL, authToken, destination, "openlist_default", client)
}

func NewOpenListDownloaderForProfile(baseURL, authToken, profileID string, client *http.Client) (*OpenListDownloader, error) {
	return NewOpenListDownloaderWithProfile(baseURL, authToken, "", profileID, client)
}

func NewOpenListDownloaderWithProfile(baseURL, authToken, destination, profileID string, client *http.Client) (*OpenListDownloader, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("OpenList base URL must be an http(s) URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(authToken) == "" {
		return nil, errors.New("OpenList Authorization token is required")
	}
	if strings.TrimSpace(destination) != "" {
		if _, err := normalizeOpenListPath(destination); err != nil {
			return nil, fmt.Errorf("OpenList default destination: %w", err)
		}
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		profileID = "openlist_default"
	}
	if client == nil {
		client = &http.Client{
			Timeout: OpenListRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &OpenListDownloader{
		baseURL:            parsed,
		authToken:          strings.TrimSpace(authToken),
		defaultDestination: strings.TrimSpace(destination),
		profileID:          profileID,
		client:             client,
	}, nil
}

func (*OpenListDownloader) Name() string { return "openlist" }
func (*OpenListDownloader) Type() string { return "openlist" }

func (d *OpenListDownloader) TargetProfile() TargetProfile {
	return TargetProfile{
		ID:                  d.profileID,
		Type:                "openlist",
		DisplayName:         "OpenList transfer and COPY",
		SupportedKinds:      []string{domain.CandidateKindCloudShare},
		SupportedGoals:      []domain.AcquisitionGoal{domain.GoalSaveToCloud},
		SupportedOperations: []domain.OperationKind{domain.OperationShareTransfer, domain.OperationOpenListCopy},
	}
}

func (*OpenListDownloader) SupportsGoal(_ domain.Candidate, goal domain.AcquisitionGoal) bool {
	return goal == domain.GoalSaveToCloud
}

func (d *OpenListDownloader) StartRequest(ctx context.Context, request StartRequest) (Handle, error) {
	if request.TargetProfile != "" && strings.TrimSpace(request.TargetProfile) != d.profileID {
		return Handle{}, fmt.Errorf("OpenList target profile %q does not match %q", request.TargetProfile, d.profileID)
	}
	if !d.SupportsGoal(request.Candidate, request.Goal) {
		return Handle{}, fmt.Errorf("OpenList does not support acquisition goal %q", request.Goal)
	}
	targetDir := strings.TrimSpace(request.TargetDir)
	if targetDir == "" {
		targetDir = d.defaultDestination
	}
	return d.startTransfer(ctx, request.JobID, request.Candidate, targetDir)
}

func (*OpenListDownloader) RequestTimeout() time.Duration {
	return OpenListRequestTimeout
}

func (*OpenListDownloader) RequiresLocalTargetDir() bool { return false }

func (d *OpenListDownloader) Supports(candidate domain.Candidate) bool {
	return d != nil && d.baseURL != nil && d.authToken != "" &&
		candidate.Kind == domain.CandidateKindCloudShare && supportedOpenListShare(candidate.RawURL)
}

func (d *OpenListDownloader) Start(ctx context.Context, jobID string, candidate domain.Candidate, targetDir string) (Handle, error) {
	if strings.TrimSpace(targetDir) == "" {
		targetDir = d.defaultDestination
	}
	return d.startTransfer(ctx, jobID, candidate, targetDir)
}

func (d *OpenListDownloader) startTransfer(ctx context.Context, jobID string, candidate domain.Candidate, targetDir string) (Handle, error) {
	if !d.Supports(candidate) {
		return Handle{}, errors.New("OpenList does not support this cloud-share candidate")
	}
	targetDir, err := normalizeOpenListPath(targetDir)
	if err != nil {
		return Handle{}, fmt.Errorf("OpenList transfer target: %w", err)
	}
	request := struct {
		URL       string `json:"url"`
		DestDir   string `json:"dst_dir"`
		ValidCode string `json:"valid_code,omitempty"`
	}{
		URL:       candidate.RawURL,
		DestDir:   targetDir,
		ValidCode: candidate.Password,
	}
	result, err := d.postWithHeaders(ctx, "/api/fs/transfer", request, mediaDockHeaders(jobID))
	if err != nil {
		if isDefinitiveOpenListRejection(result, err) {
			return Handle{}, fmt.Errorf("OpenList transfer rejected: %v", err)
		}
		return Handle{}, fmt.Errorf("%w: OpenList transfer request: %v; inspect the destination before retrying", ErrAcquisitionOutcomeUncertain, err)
	}
	if result.Code != 200 {
		message := fmt.Sprintf("OpenList returned code %d: %s", result.Code, safeMessage(result.Message))
		if isDefinitiveOpenListRejection(result, nil) {
			return Handle{}, errors.New(message)
		}
		return Handle{}, fmt.Errorf("%w: %s; inspect the destination before retrying", ErrAcquisitionOutcomeUncertain, message)
	}
	data, err := decodeTransferData(result.Data)
	if err != nil {
		return Handle{}, fmt.Errorf("%w: decode OpenList transfer result: %v; inspect the destination before retrying", ErrAcquisitionOutcomeUncertain, err)
	}
	if isFailedTransferStatus(data.Status) {
		return Handle{}, errors.New(openListOperationMessage(data, "OpenList reported that the transfer failed"))
	}
	if data.Present && data.OperationID == "" && isPendingTransferStatus(data.Status) {
		return Handle{}, fmt.Errorf("%w: OpenList accepted a transfer without an operation ID; inspect the destination before retrying", ErrAcquisitionOutcomeUncertain)
	}
	return Handle{
		RemoteID:        data.OperationID,
		RemoteOperation: domain.OperationShareTransfer,
		Ownership:       HandleOwnershipManaged,
		Completed:       !data.Present || isCompletedTransferStatus(data.Status),
		ResultKind:      "openlist_transfer",
		TargetReference: d.targetReference(data, targetDir, domain.OperationShareTransfer),
	}, nil
}

func (d *OpenListDownloader) StartCopy(ctx context.Context, request CopyRequest) (Handle, error) {
	if request.TargetProfile != "" && request.TargetProfile != d.profileID {
		return Handle{}, fmt.Errorf("OpenList target profile %q does not match %q", request.TargetProfile, d.profileID)
	}
	rawSourcePath := strings.TrimSpace(request.SourcePath)
	if rawSourcePath != "/" && strings.HasSuffix(rawSourcePath, "/") {
		return Handle{}, errors.New("OpenList COPY source must name a file, not a directory path")
	}
	sourcePath, err := normalizeOpenListPath(rawSourcePath)
	if err != nil {
		return Handle{}, fmt.Errorf("OpenList COPY source: %w", err)
	}
	if sourcePath == "/" {
		return Handle{}, errors.New("OpenList COPY source must be a file, not the root directory")
	}
	targetDir, err := normalizeOpenListPath(request.TargetDir)
	if err != nil {
		return Handle{}, fmt.Errorf("OpenList COPY target: %w", err)
	}
	if targetDir == path.Dir(sourcePath) && path.Join(targetDir, path.Base(sourcePath)) == sourcePath {
		return Handle{}, errors.New("OpenList COPY source and target are the same file")
	}
	if request.Options.Overwrite && request.Options.SkipExisting {
		return Handle{}, errors.New("OpenList COPY overwrite and skip_existing cannot both be enabled")
	}
	payload := struct {
		SourceDir string   `json:"src_dir"`
		TargetDir string   `json:"dst_dir"`
		Names     []string `json:"names"`
		Overwrite bool     `json:"overwrite"`
		Skip      bool     `json:"skip_existing"`
		Merge     bool     `json:"merge"`
	}{
		SourceDir: path.Dir(sourcePath),
		TargetDir: targetDir,
		Names:     []string{path.Base(sourcePath)},
		Overwrite: request.Options.Overwrite,
		Skip:      request.Options.SkipExisting,
		Merge:     request.Options.Merge,
	}
	result, err := d.postWithHeaders(ctx, "/api/fs/copy", payload, mediaDockHeaders(request.JobID))
	if err != nil {
		if isDefinitiveOpenListRejection(result, err) {
			return Handle{}, fmt.Errorf("OpenList COPY rejected: %v", err)
		}
		return Handle{}, fmt.Errorf("%w: OpenList COPY request: %v; inspect the target before retrying", ErrAcquisitionOutcomeUncertain, err)
	}
	if result.Code != 200 {
		message := fmt.Sprintf("OpenList COPY returned code %d: %s", result.Code, safeMessage(result.Message))
		if isDefinitiveOpenListRejection(result, nil) {
			return Handle{}, errors.New(message)
		}
		return Handle{}, fmt.Errorf("%w: %s; inspect the target before retrying", ErrAcquisitionOutcomeUncertain, message)
	}
	data, err := decodeTransferData(result.Data)
	if err != nil {
		return Handle{}, fmt.Errorf("%w: decode OpenList COPY result: %v; inspect the target before retrying", ErrAcquisitionOutcomeUncertain, err)
	}
	if len(data.Tasks) > 1 {
		return Handle{}, fmt.Errorf("%w: OpenList returned %d COPY tasks for one requested file; inspect the target before retrying", ErrAcquisitionOutcomeUncertain, len(data.Tasks))
	}
	if len(data.Tasks) == 1 {
		taskData, taskErr := data.Tasks[0].transferData()
		if taskErr != nil {
			return Handle{}, fmt.Errorf("%w: decode OpenList COPY task: %v; inspect the target before retrying", ErrAcquisitionOutcomeUncertain, taskErr)
		}
		if taskData.OperationID == "" {
			return Handle{}, fmt.Errorf("%w: OpenList returned a COPY task without an ID; inspect the target before retrying", ErrAcquisitionOutcomeUncertain)
		}
		data.OperationID = taskData.OperationID
		if data.Status == "" {
			data.Status = taskData.Status
		}
		if data.Progress == 0 && taskData.Progress != 0 {
			data.Progress = taskData.Progress
		}
		if data.Error == "" {
			data.Error = taskData.Error
		}
		if len(data.Files) == 0 {
			data.Files = taskData.Files
		}
	}
	if isFailedTransferStatus(data.Status) {
		return Handle{}, errors.New(openListOperationMessage(data, "OpenList reported that COPY failed"))
	}
	if data.Present && data.OperationID == "" && isPendingTransferStatus(data.Status) && !isSynchronousCopyResult(data) {
		return Handle{}, fmt.Errorf("%w: OpenList accepted a COPY without an operation ID; inspect the target before retrying", ErrAcquisitionOutcomeUncertain)
	}
	return Handle{
		RemoteID:        data.OperationID,
		RemoteOperation: domain.OperationOpenListCopy,
		Ownership:       HandleOwnershipManaged,
		Completed:       !data.Present || isCompletedTransferStatus(data.Status) || isSynchronousCopyResult(data),
		ResultKind:      "openlist_copy",
		TargetReference: d.targetReference(data, targetDir, domain.OperationOpenListCopy),
	}, nil
}

func (d *OpenListDownloader) TargetReference(targetDir string) *domain.TargetReference {
	if strings.TrimSpace(targetDir) == "" {
		targetDir = d.defaultDestination
	}
	targetDir, err := normalizeOpenListPath(targetDir)
	if err != nil {
		return nil
	}
	return d.targetReference(openListTransferData{}, targetDir, domain.OperationShareTransfer)
}

func (d *OpenListDownloader) Status(ctx context.Context, remoteID string) (RemoteStatus, error) {
	return d.StatusRequest(ctx, RemoteStatusRequest{RemoteID: remoteID, Operation: domain.OperationShareTransfer})
}

func (d *OpenListDownloader) StatusRequest(ctx context.Context, request RemoteStatusRequest) (RemoteStatus, error) {
	operationID := strings.TrimSpace(request.RemoteID)
	if operationID == "" {
		return RemoteStatus{}, errors.New("OpenList operation id is empty")
	}
	operation := request.Operation
	if operation == "" {
		operation = domain.OperationShareTransfer
	}
	var (
		result       openListResponse
		err          error
		legacyStatus bool
	)
	switch operation {
	case domain.OperationShareTransfer:
		result, err = d.post(ctx, "/api/fs/transfer/status", map[string]any{"operation_id": operationID})
	case domain.OperationOpenListCopy:
		// OpenList's native COPY endpoint creates a task and exposes its
		// durable state through /api/task/copy/info. The fs/copy/status
		// endpoint was never part of upstream OpenList; keep a compatibility
		// fallback for older MediaDock-aware forks only.
		result, err = d.postQuery(ctx, "/api/task/copy/info", url.Values{"tid": []string{operationID}}, nil)
		if isMissingOpenListRoute(result, err) {
			legacyStatus = true
			result, err = d.post(ctx, "/api/fs/copy/status", map[string]any{"operation_id": operationID})
		}
	default:
		return RemoteStatus{}, fmt.Errorf("OpenList does not support status operation %q", operation)
	}
	targetDir := strings.TrimSpace(request.TargetDir)
	if targetDir == "" {
		targetDir = d.defaultDestination
	}
	if err != nil {
		return RemoteStatus{}, err
	}
	if result.Code != 200 {
		return RemoteStatus{}, fmt.Errorf("OpenList %s status failed (code %d): %s", operation, result.Code, safeMessage(result.Message))
	}
	var data openListTransferData
	if operation == domain.OperationOpenListCopy && !legacyStatus {
		data, err = decodeOpenListTaskData(result.Data)
	} else {
		data, err = decodeTransferData(result.Data)
	}
	if err != nil {
		return RemoteStatus{}, fmt.Errorf("decode OpenList %s status: %w", operation, err)
	}
	if !data.Present || strings.TrimSpace(data.Status) == "" {
		return RemoteStatus{}, fmt.Errorf("OpenList %s status response did not include a status", operation)
	}
	if data.OperationID != "" && data.OperationID != operationID {
		return RemoteStatus{}, errors.New("OpenList status returned a different operation id")
	}
	data.OperationID = operationID
	status := strings.ToLower(strings.TrimSpace(data.Status))
	switch {
	case status == "cancelled" || status == "canceled":
		return RemoteStatus{Status: "cancelled", Progress: data.Progress, Message: data.Message, Error: data.Error}, nil
	case isFailedTransferStatus(status):
		return RemoteStatus{Status: "error", Progress: data.Progress, Message: data.Message, Error: data.Error}, nil
	case isCompletedTransferStatus(status):
		if operation == domain.OperationOpenListCopy {
			return RemoteStatus{Status: "completed", Progress: 1, Message: data.Message, TargetReference: d.targetReference(data, targetDir, operation)}, nil
		}
		return RemoteStatus{Status: "transferred", Progress: 1, Message: data.Message, TargetReference: d.targetReference(data, targetDir, operation)}, nil
	default:
		if operation == domain.OperationOpenListCopy {
			return RemoteStatus{Status: "copying", Progress: data.Progress, Message: data.Message, TargetReference: d.targetReference(data, targetDir, operation)}, nil
		}
		return RemoteStatus{Status: "transferring", Progress: data.Progress, Message: data.Message, TargetReference: d.targetReference(data, targetDir, operation)}, nil
	}
}

func (*OpenListDownloader) Cancel(context.Context, string) error {
	return ErrJobNotCancellable
}

func (d *OpenListDownloader) CancelRequest(ctx context.Context, request RemoteCancelRequest) error {
	if request.Operation != domain.OperationOpenListCopy {
		return ErrJobNotCancellable
	}
	operationID := strings.TrimSpace(request.RemoteID)
	if operationID == "" {
		return errors.New("OpenList COPY operation id is empty")
	}
	result, err := d.postQuery(ctx, "/api/task/copy/cancel", url.Values{"tid": []string{operationID}}, nil)
	if isMissingOpenListRoute(result, err) {
		result, err = d.post(ctx, "/api/fs/copy/cancel", map[string]any{"operation_id": operationID})
	}
	if err != nil {
		return err
	}
	if result.Code != 200 {
		return fmt.Errorf("OpenList COPY cancellation failed (code %d): %s", result.Code, safeMessage(result.Message))
	}
	return nil
}

func (d *OpenListDownloader) Reconcile(ctx context.Context, reference domain.TargetReference) (ReconcileResult, error) {
	expectedInstance := strings.TrimRight(d.baseURL.String(), "/")
	if reference.Backend != "openlist" || reference.ProfileID != d.profileID || reference.Kind != "directory" {
		return ReconcileResult{}, errors.New("OpenList target reference does not belong to this profile")
	}
	if strings.TrimSpace(reference.Instance) == "" {
		return ReconcileResult{State: "manual_verification_required", Message: "OpenList target reference has no instance; verify the external operation manually before retrying"}, nil
	}
	if strings.TrimRight(strings.TrimSpace(reference.Instance), "/") != expectedInstance {
		return ReconcileResult{}, errors.New("OpenList target reference belongs to a different OpenList instance")
	}
	if normalized, err := normalizeOpenListPath(reference.Path); err != nil || normalized != reference.Path {
		return ReconcileResult{}, errors.New("OpenList target reference has an invalid path")
	}
	if reference.OperationID == "" {
		return ReconcileResult{State: "manual_verification_required", Message: "OpenList operation has no durable ID; inspect the requested target before retrying"}, nil
	}
	status, err := d.StatusRequest(ctx, RemoteStatusRequest{RemoteID: reference.OperationID, Operation: reference.Operation, TargetDir: reference.Path})
	if err != nil {
		return ReconcileResult{}, err
	}
	if status.Status == "transferred" || status.Status == "completed" {
		return ReconcileResult{State: "completed", Message: "OpenList reports that the operation completed", TargetReference: status.TargetReference}, nil
	}
	if status.Status == "cancelled" {
		return ReconcileResult{State: "cancelled", Message: "OpenList reports that the operation was cancelled", TargetReference: status.TargetReference}, nil
	}
	if status.Status == "error" {
		message := status.Error
		if strings.TrimSpace(message) == "" {
			message = status.Message
		}
		if strings.TrimSpace(message) == "" {
			message = "OpenList reports that the operation failed"
		}
		return ReconcileResult{State: "failed", Message: safeMessage(message), TargetReference: status.TargetReference}, nil
	}
	return ReconcileResult{State: "manual_verification_required", Message: "OpenList operation is not complete; do not resubmit until its result is verified"}, nil
}

type openListResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type openListHTTPError struct {
	Status  int
	Message string
}

func (e *openListHTTPError) Error() string {
	if e == nil {
		return "OpenList HTTP request failed"
	}
	return e.Message
}

func isDefinitiveOpenListRejection(result openListResponse, requestErr error) bool {
	if result.Code >= 400 && result.Code < 500 {
		return true
	}
	var httpErr *openListHTTPError
	return errors.As(requestErr, &httpErr) && httpErr.Status >= 400 && httpErr.Status < 500
}

func isMissingOpenListRoute(result openListResponse, requestErr error) bool {
	// OpenList's common error responses use HTTP 200 with a non-200 JSON code.
	// A missing native route, however, is normally an actual HTTP 404 (often
	// with a non-JSON body). Do not treat a valid task-not-found response as a
	// signal to call a second endpoint.
	if result.Code != 0 {
		return false
	}
	var httpErr *openListHTTPError
	return errors.As(requestErr, &httpErr) && httpErr.Status == http.StatusNotFound
}

type openListTransferData struct {
	Present     bool                   `json:"-"`
	OperationID string                 `json:"operation_id"`
	TaskID      string                 `json:"task_id"`
	Status      string                 `json:"status"`
	Progress    float64                `json:"progress"`
	Message     string                 `json:"message"`
	Error       string                 `json:"error"`
	Files       []domain.FileReference `json:"files"`
	Tasks       []openListTaskInfo     `json:"tasks"`
}

type openListTaskInfo struct {
	ID       string                 `json:"id"`
	State    json.RawMessage        `json:"state"`
	Status   string                 `json:"status"`
	Progress float64                `json:"progress"`
	Error    string                 `json:"error"`
	Files    []domain.FileReference `json:"files"`
}

func (d openListTransferData) operationID() string {
	if strings.TrimSpace(d.OperationID) != "" {
		return strings.TrimSpace(d.OperationID)
	}
	return strings.TrimSpace(d.TaskID)
}

func decodeTransferData(raw json.RawMessage) (openListTransferData, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return openListTransferData{}, nil
	}
	var data openListTransferData
	if err := json.Unmarshal(trimmed, &data); err != nil {
		return openListTransferData{}, err
	}
	data.Present = true
	data.OperationID = data.operationID()
	if data.Progress < 0 || data.Progress > 1 {
		return openListTransferData{}, fmt.Errorf("progress must be between 0 and 1")
	}
	data.Status = strings.ToLower(strings.TrimSpace(data.Status))
	return data, nil
}

func decodeOpenListTaskData(raw json.RawMessage) (openListTransferData, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return openListTransferData{}, nil
	}
	var task openListTaskInfo
	if err := json.Unmarshal(trimmed, &task); err != nil {
		return openListTransferData{}, err
	}
	return task.transferData()
}

func (task openListTaskInfo) transferData() (openListTransferData, error) {
	progress, err := normalizeOpenListProgress(task.Progress)
	if err != nil {
		return openListTransferData{}, err
	}
	status := strings.ToLower(strings.TrimSpace(task.Status))
	if stateStatus := openListTaskStateStatus(task.State); stateStatus != "" {
		switch stateStatus {
		case "completed", "cancelled", "failed":
			status = stateStatus
		case "pending":
			if status == "" {
				status = stateStatus
			}
		}
	}
	return openListTransferData{
		Present:     true,
		OperationID: strings.TrimSpace(task.ID),
		Status:      status,
		Progress:    progress,
		Error:       strings.TrimSpace(task.Error),
		Files:       task.Files,
	}, nil
}

func normalizeOpenListProgress(progress float64) (float64, error) {
	if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 || progress > 100 {
		return 0, fmt.Errorf("progress must be between 0 and 1 (or 0 and 100 for native OpenList task responses)")
	}
	if progress > 1 {
		progress /= 100
	}
	return progress, nil
}

func openListTaskStateStatus(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var state int
	if err := json.Unmarshal(trimmed, &state); err == nil {
		switch state {
		case 2:
			return "completed" // tache.StateSucceeded
		case 4:
			return "cancelled" // tache.StateCanceled
		case 7:
			return "failed" // tache.StateFailed
		default:
			return "pending"
		}
	}
	var stateName string
	if err := json.Unmarshal(trimmed, &stateName); err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(stateName)) {
	case "succeeded", "success", "completed":
		return "completed"
	case "cancelled", "canceled":
		return "cancelled"
	case "failed", "error":
		return "failed"
	default:
		return "pending"
	}
}

func isSynchronousCopyResult(data openListTransferData) bool {
	message := strings.ToLower(strings.TrimSpace(data.Message))
	return strings.Contains(message, "copy operations completed immediately")
}

func mediaDockHeaders(jobID string) map[string]string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	return map[string]string{
		"Idempotency-Key":    jobID,
		"X-MediaDock-Job-ID": jobID,
	}
}

func isPendingTransferStatus(status string) bool {
	return !isCompletedTransferStatus(status) && !isFailedTransferStatus(status)
}

func isCompletedTransferStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "success", "succeeded", "transferred", "copied":
		return true
	default:
		return false
	}
}

func isFailedTransferStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func openListOperationMessage(data openListTransferData, fallback string) string {
	message := strings.TrimSpace(data.Error)
	if message == "" {
		message = strings.TrimSpace(data.Message)
	}
	if message == "" {
		message = fallback
	}
	return safeMessage(message)
}

func (d *OpenListDownloader) targetReference(data openListTransferData, targetDir string, operation domain.OperationKind) *domain.TargetReference {
	if normalized, err := normalizeOpenListPath(targetDir); err == nil {
		targetDir = normalized
	}
	reference := &domain.TargetReference{
		Backend:     "openlist",
		Instance:    strings.TrimRight(d.baseURL.String(), "/"),
		ProfileID:   d.profileID,
		Kind:        "directory",
		Operation:   operation,
		Path:        targetDir,
		OperationID: data.OperationID,
		ObservedAt:  time.Now().UTC(),
	}
	for _, file := range data.Files {
		filePath := strings.TrimSpace(file.Path)
		if !isAllowedOpenListFilePath(targetDir, filePath) {
			continue
		}
		file.Path = path.Clean(filePath)
		if strings.TrimSpace(file.Name) == "" {
			file.Name = path.Base(file.Path)
		}
		reference.Files = append(reference.Files, file)
	}
	return reference
}

func normalizeOpenListPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", errors.New("path must use OpenList slash separators without NUL or backslash")
	}
	if !path.IsAbs(value) {
		return "", errors.New("path must be absolute")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("path must not contain dot or parent-directory segments")
		}
	}
	return path.Clean(value), nil
}

func isAllowedOpenListFilePath(targetDir, filePath string) bool {
	normalizedFile, err := normalizeOpenListPath(filePath)
	if err != nil {
		return false
	}
	normalizedTarget, err := normalizeOpenListPath(targetDir)
	if err != nil {
		return false
	}
	if normalizedTarget == "/" {
		return true
	}
	return normalizedFile != normalizedTarget && strings.HasPrefix(normalizedFile, normalizedTarget+"/")
}

func (d *OpenListDownloader) post(ctx context.Context, endpoint string, payload any) (openListResponse, error) {
	return d.postWithQueryAndHeaders(ctx, endpoint, nil, payload, nil)
}

func (d *OpenListDownloader) postQuery(ctx context.Context, endpoint string, query url.Values, payload any) (openListResponse, error) {
	return d.postWithQueryAndHeaders(ctx, endpoint, query, payload, nil)
}

func (d *OpenListDownloader) postWithHeaders(ctx context.Context, endpoint string, payload any, headers map[string]string) (openListResponse, error) {
	return d.postWithQueryAndHeaders(ctx, endpoint, nil, payload, headers)
}

func (d *OpenListDownloader) postWithQueryAndHeaders(ctx context.Context, endpoint string, query url.Values, payload any, headers map[string]string) (openListResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return openListResponse{}, fmt.Errorf("encode request: %w", err)
	}
	requestURL := *d.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + endpoint
	requestURL.RawPath = ""
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return openListResponse{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", d.authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "media-dock/0.1")
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return openListResponse{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return openListResponse{}, fmt.Errorf("read response: %w", err)
	}
	var result openListResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return result, &openListHTTPError{Status: resp.StatusCode, Message: fmt.Sprintf("HTTP %d with an invalid JSON response", resp.StatusCode)}
		}
		return result, fmt.Errorf("decode response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, &openListHTTPError{Status: resp.StatusCode, Message: fmt.Sprintf("HTTP %d (code %d): %s", resp.StatusCode, result.Code, safeMessage(result.Message))}
	}
	return result, nil
}

func supportedOpenListShare(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "pan.quark.cn", "www.alipan.com", "www.aliyundrive.com", "pan.baidu.com", "yun.baidu.com":
	default:
		return false
	}
	if !strings.HasPrefix(u.Path, "/s/") {
		return false
	}
	id := strings.TrimPrefix(u.Path, "/s/")
	if id == "" || strings.Contains(id, "/") {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func safeMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}
