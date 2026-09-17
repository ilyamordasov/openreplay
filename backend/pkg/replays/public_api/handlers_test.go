package public_api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

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
	exists bool
	err    error
}

func (f *fakeSessions) GetReplay(uint32, uint64, string) (*session.SessionReplay, error) {
	return nil, f.err
}
func (f *fakeSessions) IsExists(uint32, uint64) (bool, error) { return f.exists, f.err }
func (f *fakeSessions) GetPlatform(uint32, uint64) (string, error) {
	return "web", f.err
}
func (f *fakeSessions) GetFileKey(uint64) (*string, error) { return nil, f.err }

type fakeFiles struct {
	archive []byte
	err     error
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
	if f.err != nil {
		return f.err
	}
	_, err := w.Write(f.archive)
	return err
}

func publicDownloadRequest(projectKey, sessionID string, tenantID int) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = mux.SetURLVars(req, map[string]string{
		"project":   projectKey,
		"sessionID": sessionID,
	})
	ctx := context.WithValue(req.Context(), "tenantData", &tenant.Tenant{TenantID: tenantID})
	return req.WithContext(ctx)
}

func TestPublicSessionDownload(t *testing.T) {
	const (
		projectKey = "project-key"
		sessionID  = "4020541843067130369"
		tenantID   = 7
	)

	h := &handlersImpl{
		projects: &fakeProjects{project: &projects.Project{
			ProjectID:  42,
			ProjectKey: projectKey,
			TenantID:   tenantID,
		}},
		sessions: &fakeSessions{exists: true},
		files:    &fakeFiles{archive: []byte("zip-data")},
	}

	rr := httptest.NewRecorder()
	h.downloadSession(rr, publicDownloadRequest(projectKey, sessionID, tenantID))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type = %q, want application/zip", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, sessionID+".zip") {
		t.Fatalf("Content-Disposition = %q, want session archive filename", got)
	}
	if got := rr.Body.String(); got != "zip-data" {
		t.Fatalf("body = %q, want zip-data", got)
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
