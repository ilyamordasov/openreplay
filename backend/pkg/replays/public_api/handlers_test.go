package public_api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"openreplay/backend/pkg/logger"
	"openreplay/backend/pkg/projects"
	"openreplay/backend/pkg/replays/service"
	serverapi "openreplay/backend/pkg/server/api"
	"openreplay/backend/pkg/server/tenant"
	"openreplay/backend/pkg/session"
)

type fakeProjects struct {
	project *projects.Project
	err     error
}

func (f *fakeProjects) GetProject(uint32) (*projects.Project, error) { return f.project, f.err }
func (f *fakeProjects) GetProjectByKey(string) (*projects.Project, error) {
	return f.project, f.err
}
func (f *fakeProjects) GetProjectByKeyAndTenant(projectKey string, tenantID int) (*projects.Project, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.project == nil || f.project.ProjectKey != projectKey || f.project.TenantID != tenantID {
		return nil, errors.New("project not found")
	}
	return f.project, nil
}
func (f *fakeProjects) GetProjectNotDeleted(uint32) (*projects.Project, error) {
	return f.project, f.err
}
func (f *fakeProjects) ListProjectsByTenantID(int) ([]*projects.Project, error) {
	if f.project == nil {
		return nil, f.err
	}
	return []*projects.Project{f.project}, f.err
}
func (f *fakeProjects) ExistsByName(string, int) (bool, error) { return false, f.err }
func (f *fakeProjects) CreateProject(int, string, string) (*projects.Project, error) {
	return f.project, f.err
}

type fakeSessions struct {
	exists    bool
	err       error
	calls     int
	projectID uint32
	sessionID uint64
}

func (f *fakeSessions) GetReplay(uint32, uint64, string) (*session.SessionReplay, error) {
	return nil, f.err
}
func (f *fakeSessions) IsExists(projectID uint32, sessionID uint64) (bool, error) {
	f.calls++
	f.projectID = projectID
	f.sessionID = sessionID
	return f.exists, f.err
}
func (f *fakeSessions) GetPlatform(uint32, uint64) (string, error) {
	return "web", f.err
}
func (f *fakeSessions) GetFileKey(uint64) (*string, error) { return nil, f.err }

type fakeFiles struct {
	archive       []byte
	err           error
	afterWriteErr error
	calls         int
}

func (f *fakeFiles) GetMobsUrls(uint64) ([]string, error)             { return nil, nil }
func (f *fakeFiles) GetDevtoolsUrls(uint64) ([]string, error)         { return nil, nil }
func (f *fakeFiles) GetMobStartUrl(uint64) ([]string, error)          { return nil, nil }
func (f *fakeFiles) GetCanvasUrls(uint64) ([]string, []string, error) { return nil, nil, nil }
func (f *fakeFiles) GetMobileReplayUrls(uint64) ([]string, []string, error) {
	return nil, nil, nil
}
func (f *fakeFiles) GetUnprocessedMob(uint64) (string, error)      { return "", nil }
func (f *fakeFiles) GetUnprocessedMobE(uint64) (string, error)     { return "", nil }
func (f *fakeFiles) GetUnprocessedDevtools(uint64) (string, error) { return "", nil }
func (f *fakeFiles) WriteSessionArchive(_ uint64, w io.Writer) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if len(f.archive) > 0 {
		if _, err := w.Write(f.archive); err != nil {
			return err
		}
	}
	return f.afterWriteErr
}

type publicTestLogger struct{}

func (publicTestLogger) Debug(context.Context, string, ...interface{}) {}
func (publicTestLogger) Info(context.Context, string, ...interface{})  {}
func (publicTestLogger) Warn(context.Context, string, ...interface{})  {}
func (publicTestLogger) Error(context.Context, string, ...interface{}) {}
func (publicTestLogger) Fatal(context.Context, string, ...interface{}) {}

func publicDownloadRequest(projectKey, sessionID string, tenantID int) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = mux.SetURLVars(req, map[string]string{
		"project":   projectKey,
		"sessionID": sessionID,
	})
	ctx := context.WithValue(req.Context(), "tenantData", &tenant.Tenant{TenantID: tenantID})
	return req.WithContext(ctx)
}

func makePublicHandlerTestZIP(t *testing.T, sessionID string) []byte {
	t.Helper()

	var output bytes.Buffer
	archive := zip.NewWriter(&output)

	manifest, err := archive.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(manifest, "{\"sessionId\":\""+sessionID+"\"}\n"); err != nil {
		t.Fatal(err)
	}

	raw, err := archive.Create("raw/dom.mobs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Write([]byte{0, 1, 2, 255}); err != nil {
		t.Fatal(err)
	}

	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func assertPublicAbortHandler(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("panic = %v, want http.ErrAbortHandler", got)
		}
	}()
	fn()
}

var errPublicZeroByteWrite = errors.New("injected zero-byte write failure")

type publicZeroByteFailingResponseWriter struct {
	header http.Header
	status int
}

func (w *publicZeroByteFailingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *publicZeroByteFailingResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *publicZeroByteFailingResponseWriter) Write([]byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return 0, errPublicZeroByteWrite
}

func TestPublicSessionDownload(t *testing.T) {
	const (
		projectKey = "project-key"
		sessionID  = "4020541843067130369"
		tenantID   = 7
	)

	sessions := &fakeSessions{exists: true}
	files := &fakeFiles{archive: makePublicHandlerTestZIP(t, sessionID)}
	h := &handlersImpl{
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: sessions,
		files:    files,
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, publicDownloadRequest(projectKey, sessionID, tenantID))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", got)
	}
	const wantDisposition = "attachment; filename=\"openreplay-session-4020541843067130369.zip\""
	if got := rr.Header().Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("Content-Disposition = %q, want %q", got, wantDisposition)
	}
	disposition, params, err := mime.ParseMediaType(rr.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	if disposition != "attachment" || params["filename"] != "openreplay-session-"+sessionID+".zip" {
		t.Fatalf("parsed Content-Disposition = %q %v", disposition, params)
	}
	if got := rr.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}

	reader, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatalf("response is not a valid ZIP: %v", err)
	}
	if len(reader.File) != 2 {
		t.Fatalf("ZIP entries = %d, want 2", len(reader.File))
	}
	if sessions.calls != 1 || sessions.projectID != 42 || sessions.sessionID != 4020541843067130369 {
		t.Fatalf("session lookup = calls:%d project:%d session:%d", sessions.calls, sessions.projectID, sessions.sessionID)
	}
	if files.calls != 1 {
		t.Fatalf("archive calls = %d, want 1", files.calls)
	}
}

func TestPublicSessionDownloadRejectsInvalidSessionIDsBeforeLookup(t *testing.T) {
	const (
		projectKey = "project-key"
		tenantID   = 7
	)

	for _, sessionID := range []string{"0", "-1", "18446744073709551616", "not-a-number"} {
		t.Run(sessionID, func(t *testing.T) {
			sessions := &fakeSessions{exists: true}
			files := &fakeFiles{archive: []byte("must-not-download")}
			h := &handlersImpl{
				projects: &fakeProjects{project: &projects.Project{
					ProjectID:  42,
					ProjectKey: projectKey,
					TenantID:   tenantID,
				}},
				sessions: sessions,
				files:    files,
			}

			rr := httptest.NewRecorder()
			h.downloadSession(rr, publicDownloadRequest(projectKey, sessionID, tenantID))

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
			}
			if sessions.calls != 0 {
				t.Fatalf("session lookup calls = %d, want 0", sessions.calls)
			}
			if files.calls != 0 {
				t.Fatalf("archive calls = %d, want 0", files.calls)
			}
		})
	}
}

func TestPublicSessionDownloadRejectsSessionOutsideProject(t *testing.T) {
	const (
		projectKey = "project-key"
		tenantID   = 7
	)

	h := &handlersImpl{
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: &fakeSessions{exists: false},
		files:    &fakeFiles{archive: []byte("must-not-download")},
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, publicDownloadRequest(projectKey, "123", tenantID))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if strings.Contains(rr.Body.String(), "must-not-download") {
		t.Fatal("archive was returned for a session outside the project")
	}
}

func TestPublicSessionDownloadRejectsProjectOutsideTenant(t *testing.T) {
	const projectKey = "project-key"

	h := &handlersImpl{
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   99,
		}},
		sessions: &fakeSessions{exists: true},
		files:    &fakeFiles{archive: []byte("must-not-download")},
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, publicDownloadRequest(projectKey, "123", 7))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestPublicSessionDownloadReturnsNotFoundWhenArchiveIsMissing(t *testing.T) {
	const (
		projectKey = "project-key"
		tenantID   = 7
	)

	h := &handlersImpl{
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: &fakeSessions{exists: true},
		files:    &fakeFiles{err: service.ErrSessionArchiveNotFound},
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, publicDownloadRequest(projectKey, "123", tenantID))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want empty", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want empty", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
}

func TestPublicSessionDownloadReturnsInternalErrorBeforeStreaming(t *testing.T) {
	const (
		projectKey = "project-key"
		tenantID   = 7
	)

	h := &handlersImpl{
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: &fakeSessions{exists: true},
		files:    &fakeFiles{err: errors.New("storage failure")},
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, publicDownloadRequest(projectKey, "123", tenantID))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want empty", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("Cache-Control = %q, want empty", got)
	}
}

func TestPublicSessionDownloadAbortsAfterStreamStarts(t *testing.T) {
	const (
		projectKey = "project-key"
		tenantID   = 7
	)

	h := &handlersImpl{
		log: publicTestLogger{},
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: &fakeSessions{exists: true},
		files: &fakeFiles{
			archive:       []byte("partial"),
			afterWriteErr: errors.New("injected stream failure"),
		},
	}

	rr := httptest.NewRecorder()
	assertPublicAbortHandler(t, func() {
		h.downloadSession(rr, publicDownloadRequest(projectKey, "123", tenantID))
	})

	if got := rr.Body.String(); got != "partial" {
		t.Fatalf("body = %q, want partial", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", got)
	}
}

func TestPublicSessionDownloadAbortsOnZeroByteFailedFirstWrite(t *testing.T) {
	const (
		projectKey = "project-key"
		tenantID   = 7
	)

	files := &fakeFiles{archive: []byte("first-write")}
	h := &handlersImpl{
		log: publicTestLogger{},
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: &fakeSessions{exists: true},
		files:    files,
	}

	writer := &publicZeroByteFailingResponseWriter{}
	assertPublicAbortHandler(t, func() {
		h.downloadSession(writer, publicDownloadRequest(projectKey, "123", tenantID))
	})

	if files.calls != 1 {
		t.Fatalf("archive calls = %d, want 1", files.calls)
	}
	if got := writer.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("archive headers were unexpectedly cleared after a started write")
	}
}

func TestPublicSessionDownloadRouteUsesApiKeyAuth(t *testing.T) {
	h := &handlersImpl{}
	routes := h.GetAll()
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	route := routes[0]
	if route.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", route.Method)
	}
	if route.Path != "/public/{project}/sessions/{sessionID}/download" {
		t.Fatalf("path = %q", route.Path)
	}
	if len(route.Permissions) != 1 || route.Permissions[0] != serverapi.PublicKeyPermission {
		t.Fatalf("permissions = %v, want API key permission", route.Permissions)
	}
}

var _ logger.Logger = publicTestLogger{}
var _ session.Service = (*fakeSessions)(nil)
var _ service.Files = (*fakeFiles)(nil)
