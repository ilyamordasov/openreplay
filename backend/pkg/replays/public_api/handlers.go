package public_api

import (
	"errors"
	"fmt"
	"net/http"

	"openreplay/backend/pkg/logger"
	"openreplay/backend/pkg/projects"
	"openreplay/backend/pkg/replays/service"
	"openreplay/backend/pkg/server/api"
	"openreplay/backend/pkg/session"
)

type handlersImpl struct {
	log      logger.Logger
	projects projects.Projects
	sessions session.Service
	files    service.Files
}

type archiveWriteTracker struct {
	writer  http.ResponseWriter
	written int64
	started bool
}

func (w *archiveWriteTracker) Write(p []byte) (int, error) {
	// Even a failed write can commit HTTP headers.
	w.started = true
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}

func NewHandlers(log logger.Logger, projects projects.Projects, sessions session.Service, files service.Files) (api.Handlers, error) {
	return &handlersImpl{
		log:      log,
		projects: projects,
		sessions: sessions,
		files:    files,
	}, nil
}

func (h *handlersImpl) GetAll() []*api.Description {
	return []*api.Description{
		{"/public/{project}/sessions/{sessionID}/download", "GET", h.downloadSession, []string{api.PublicKeyPermission}, api.DoNotTrack},
	}
}

func (h *handlersImpl) downloadSession(w http.ResponseWriter, r *http.Request) {
	projectKey, err := api.GetParam(r, "project")
	if err != nil {
		http.Error(w, "missing project", http.StatusBadRequest)
		return
	}
	tenantID, err := api.GetTenantID(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	project, err := h.projects.GetProjectByKeyAndTenant(projectKey, tenantID)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	sessID, err := api.GetPathParam(r, "sessionID", api.ParseUint64)
	if err != nil {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}
	exists, err := h.sessions.IsExists(project.ProjectID, sessID)
	if err != nil {
		h.log.Error(r.Context(), "failed to validate session ownership, project: %d, session: %d, err: %v", project.ProjectID, sessID, err)
		http.Error(w, "failed to validate session", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"openreplay-session-%d.zip\"", sessID))
	w.Header().Set("Cache-Control", "private, no-store")

	tracker := &archiveWriteTracker{writer: w}
	if err := h.files.WriteSessionArchive(sessID, tracker); err != nil {
		if !tracker.started {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Cache-Control")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			status := http.StatusInternalServerError
			message := "failed to build session archive"
			if errors.Is(err, service.ErrSessionArchiveNotFound) {
				status = http.StatusNotFound
				message = "session archive not found"
			}
			http.Error(w, message, status)
			return
		}
		h.log.Error(r.Context(), "failed to stream public session archive after %d bytes, session: %d, err: %v", tracker.written, sessID, err)
		panic(http.ErrAbortHandler)
	}
}
