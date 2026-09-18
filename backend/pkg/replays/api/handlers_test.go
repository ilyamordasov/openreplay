package api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"openreplay/backend/pkg/logger"
	"openreplay/backend/pkg/replays/service"
	serverapi "openreplay/backend/pkg/server/api"
	"openreplay/backend/pkg/session"
)

type testLogger struct{}

func (testLogger) Debug(context.Context, string, ...interface{}) {}
func (testLogger) Info(context.Context, string, ...interface{})  {}
func (testLogger) Warn(context.Context, string, ...interface{})  {}
func (testLogger) Error(context.Context, string, ...interface{}) {}
func (testLogger) Fatal(context.Context, string, ...interface{}) {}

type testResponser struct{}

func (testResponser) ResponseOK(_ logger.Logger, _ context.Context, w http.ResponseWriter, _ time.Time, _ string, _ int) {
	w.WriteHeader(http.StatusOK)
}

func (testResponser) ResponseWithJSON(_ logger.Logger, _ context.Context, w http.ResponseWriter, _ interface{}, _ time.Time, _ string, _ int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
}

func (testResponser) ResponseWithError(_ logger.Logger, _ context.Context, w http.ResponseWriter, code int, _ error, _ time.Time, _ string, _ int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
}

type testSessions struct {
	exists    bool
	err       error
	calls     int
	projectID uint32
	sessionID uint64
}

func (s *testSessions) GetReplay(uint32, uint64, string) (*session.SessionReplay, error) {
	return nil, s.err
}

func (s *testSessions) IsExists(projectID uint32, sessionID uint64) (bool, error) {
	s.calls++
	s.projectID = projectID
	s.sessionID = sessionID
	return s.exists, s.err
}

func (s *testSessions) GetPlatform(uint32, uint64) (string, error) {
	return "web", s.err
}

func (s *testSessions) GetFileKey(uint64) (*string, error) {
	return nil, s.err
}

type testFiles struct {
	archive       []byte
	err           error
	afterWriteErr error
	calls         int
}

func (f *testFiles) GetMobsUrls(uint64) ([]string, error)             { return nil, nil }
func (f *testFiles) GetDevtoolsUrls(uint64) ([]string, error)         { return nil, nil }
func (f *testFiles) GetMobStartUrl(uint64) ([]string, error)          { return nil, nil }
func (f *testFiles) GetCanvasUrls(uint64) ([]string, []string, error) { return nil, nil, nil }
func (f *testFiles) GetMobileReplayUrls(uint64) ([]string, []string, error) {
	return nil, nil, nil
}
func (f *testFiles) GetUnprocessedMob(uint64) (string, error)      { return "", nil }
func (f *testFiles) GetUnprocessedMobE(uint64) (string, error)     { return "", nil }
func (f *testFiles) GetUnprocessedDevtools(uint64) (string, error) { return "", nil }

func (f *testFiles) WriteSessionArchive(_ uint64, w io.Writer) error {
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

func applicationDownloadRequest(projectID, sessionID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	return mux.SetURLVars(req, map[string]string{
		"project": projectID,
		"session": sessionID,
	})
}

func makeHandlerTestZIP(t *testing.T, sessionID string) []byte {
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

func assertAbortHandler(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("panic = %v, want http.ErrAbortHandler", got)
		}
	}()
	fn()
}

var errZeroByteWrite = errors.New("injected zero-byte write failure")

type zeroByteFailingResponseWriter struct {
	header http.Header
	status int
}

func (w *zeroByteFailingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *zeroByteFailingResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *zeroByteFailingResponseWriter) Write([]byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return 0, errZeroByteWrite
}

func TestSessionDownloadReturnsExactHeadersAndZIP(t *testing.T) {
	const (
		projectID = "42"
		sessionID = "4020541843067130369"
	)

	sessions := &testSessions{exists: true}
	files := &testFiles{archive: makeHandlerTestZIP(t, sessionID)}
	h := &handlersImpl{
		log:       testLogger{},
		responser: testResponser{},
		sessions:  sessions,
		files:     files,
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, applicationDownloadRequest(projectID, sessionID))

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

func TestSessionDownloadRejectsInvalidIDsBeforeLookup(t *testing.T) {
	tests := []struct {
		name      string
		projectID string
		sessionID string
	}{
		{name: "zero project", projectID: "0", sessionID: "1"},
		{name: "negative project", projectID: "-1", sessionID: "1"},
		{name: "project overflow", projectID: "4294967296", sessionID: "1"},
		{name: "invalid project", projectID: "not-a-number", sessionID: "1"},
		{name: "zero session", projectID: "1", sessionID: "0"},
		{name: "negative session", projectID: "1", sessionID: "-1"},
		{name: "session overflow", projectID: "1", sessionID: "18446744073709551616"},
		{name: "invalid session", projectID: "1", sessionID: "not-a-number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &testSessions{exists: true}
			files := &testFiles{archive: []byte("must-not-write")}
			h := &handlersImpl{
				log:       testLogger{},
				responser: testResponser{},
				sessions:  sessions,
				files:     files,
			}

			rr := httptest.NewRecorder()
			h.downloadSession(rr, applicationDownloadRequest(tt.projectID, tt.sessionID))

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

func TestSessionDownloadPreStreamFailureClearsArchiveHeaders(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "archive missing", err: service.ErrSessionArchiveNotFound, status: http.StatusNotFound},
		{name: "storage failure", err: errors.New("storage failure"), status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &handlersImpl{
				log:       testLogger{},
				responser: testResponser{},
				sessions:  &testSessions{exists: true},
				files:     &testFiles{err: tt.err},
			}

			rr := httptest.NewRecorder()
			h.downloadSession(rr, applicationDownloadRequest("42", "123"))

			if rr.Code != tt.status {
				t.Fatalf("status = %d, want %d", rr.Code, tt.status)
			}
			if got := rr.Header().Get("Content-Disposition"); got != "" {
				t.Fatalf("Content-Disposition = %q, want empty", got)
			}
			if got := rr.Header().Get("Cache-Control"); got != "" {
				t.Fatalf("Cache-Control = %q, want empty", got)
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
		})
	}
}

func TestSessionDownloadAbortsAfterStreamStarts(t *testing.T) {
	streamErr := errors.New("injected stream failure")
	h := &handlersImpl{
		log:       testLogger{},
		responser: testResponser{},
		sessions:  &testSessions{exists: true},
		files: &testFiles{
			archive:       []byte("partial"),
			afterWriteErr: streamErr,
		},
	}

	rr := httptest.NewRecorder()
	assertAbortHandler(t, func() {
		h.downloadSession(rr, applicationDownloadRequest("42", "123"))
	})

	if got := rr.Body.String(); got != "partial" {
		t.Fatalf("body = %q, want partial", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", got)
	}
}

func TestSessionDownloadAbortsOnZeroByteFailedFirstWrite(t *testing.T) {
	files := &testFiles{archive: []byte("first-write")}
	h := &handlersImpl{
		log:       testLogger{},
		responser: testResponser{},
		sessions:  &testSessions{exists: true},
		files:     files,
	}

	writer := &zeroByteFailingResponseWriter{}
	assertAbortHandler(t, func() {
		h.downloadSession(writer, applicationDownloadRequest("42", "123"))
	})

	if files.calls != 1 {
		t.Fatalf("archive calls = %d, want 1", files.calls)
	}
	if got := writer.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("archive headers were unexpectedly cleared after a started write")
	}
}

var _ logger.Logger = testLogger{}
var _ serverapi.Responser = testResponser{}
var _ session.Service = (*testSessions)(nil)
var _ service.Files = (*testFiles)(nil)
