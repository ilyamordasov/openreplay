package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"os"
	"path/filepath"
	"time"

	"openreplay/backend/pkg/logger"
	"openreplay/backend/pkg/replays/service"
	"openreplay/backend/pkg/server/api"
	"openreplay/backend/pkg/session"
)

type handlersImpl struct {
	log       logger.Logger
	responser api.Responser
	sessions  session.Service
	files     service.Files
}

const maxStitchedSessionGroupSize = 1000

type stitchedSessionDownloadRequest struct {
	SessionIDs []string `json:"sessionIds"`
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

func (h *handlersImpl) GetAll() []*api.Description {
	return []*api.Description{
		{"/{project}/sessions/{session}/first-mob", "GET", h.getFirstMob, []string{"SESSION_REPLAY", "SERVICE_SESSION_REPLAY"}, "get_first_mob_file"},
		{"/{project}/sessions/{session}/download", "GET", h.downloadSession, []string{"SESSION_REPLAY", "SERVICE_SESSION_REPLAY"}, "download_session"},
		{"/{project}/sessions/download", "POST", h.downloadSessionGroup, []string{"SESSION_REPLAY", "SERVICE_SESSION_REPLAY"}, "download_session_group"},
		{"/{project}/unprocessed/{session}/dom.mob", "GET", h.getUnprocessedMob, []string{"SESSION_REPLAY", "SERVICE_SESSION_REPLAY", "ASSIST_LIVE", "SERVICE_ASSIST_LIVE"}, "get_unprocessed_mob_file"},
		{"/{project}/unprocessed/{session}/devtools.mob", "GET", h.getUnprocessedDevtools, []string{"SESSION_REPLAY", "SERVICE_SESSION_REPLAY", "ASSIST_LIVE", "SERVICE_ASSIST_LIVE", "DEV_TOOLS", "SERVICE_DEV_TOOLS"}, "get_unprocessed_devtools_file"},
	}
}

func NewHandlers(log logger.Logger, responser api.Responser, sessions session.Service, files service.Files) (api.Handlers, error) {
	return &handlersImpl{
		log:       log,
		responser: responser,
		sessions:  sessions,
		files:     files,
	}, nil
}

func (h *handlersImpl) getFirstMob(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	bodySize := 0

	projID, err := api.GetProject(r)
	if err != nil {
		h.log.Error(r.Context(), "Error getting project ID: %v", err)
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong project id"), startTime, r.URL.Path, bodySize)
		return
	}
	sessID, err := api.GetSessionID(r)
	if err != nil {
		h.log.Error(r.Context(), "Error getting session ID: %v", err)
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong session id"), startTime, r.URL.Path, bodySize)
		return
	}

	h.log.Info(r.Context(), "getFirstMob: sessID: %v, projID: %v", sessID, projID)

	isSessionExists, err := h.sessions.IsExists(projID, sessID)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusInternalServerError, err, startTime, r.URL.Path, bodySize)
		return
	}
	if !isSessionExists {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong session id"), startTime, r.URL.Path, bodySize)
		return
	}

	urls, err := h.files.GetMobStartUrl(sessID)
	if err != nil {
		h.log.Error(r.Context(), "Error getting start urls: %v", err)
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong session id"), startTime, r.URL.Path, bodySize)
		return
	}

	fileKey, err := h.sessions.GetFileKey(sessID)
	if err != nil {
		h.log.Error(r.Context(), "Error getting file key: %v", err)
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("error retrieving file key"), startTime, r.URL.Path, bodySize)
		return
	}

	res := map[string]interface{}{"domURL": urls, "fileKey": fileKey}

	h.responser.ResponseWithJSON(h.log, r.Context(), w, map[string]interface{}{"data": res}, startTime, r.URL.Path, bodySize)
}

func (h *handlersImpl) downloadSession(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	bodySize := 0

	projID, err := api.GetPathParam(r, "project", api.ParseUint32)
	if err != nil || projID == 0 {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong project id"), startTime, r.URL.Path, bodySize)
		return
	}
	sessID, err := api.GetPathParam(r, "session", api.ParseUint64)
	if err != nil || sessID == 0 {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong session id"), startTime, r.URL.Path, bodySize)
		return
	}

	exists, err := h.sessions.IsExists(projID, sessID)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusInternalServerError, err, startTime, r.URL.Path, bodySize)
		return
	}
	if !exists {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusNotFound, errors.New("session not found"), startTime, r.URL.Path, bodySize)
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
			w.Header().Set("Content-Type", "application/json")
			status := http.StatusInternalServerError
			if errors.Is(err, service.ErrSessionArchiveNotFound) {
				status = http.StatusNotFound
			}
			h.responser.ResponseWithError(h.log, r.Context(), w, status, err, startTime, r.URL.Path, bodySize)
			return
		}
		h.log.Error(r.Context(), "failed to stream session archive after %d bytes, session: %d, err: %v", tracker.written, sessID, err)
		panic(http.ErrAbortHandler)
	}
}

func (h *handlersImpl) downloadSessionGroup(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	bodySize := 0

	projID, err := api.GetPathParam(r, "project", api.ParseUint32)
	if err != nil || projID == 0 {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("wrong project id"), startTime, r.URL.Path, bodySize)
		return
	}

	var payload stitchedSessionDownloadRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 256*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("invalid stitched session payload"), startTime, r.URL.Path, bodySize)
		return
	}
	if len(payload.SessionIDs) == 0 || len(payload.SessionIDs) > maxStitchedSessionGroupSize {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("invalid stitched session group size"), startTime, r.URL.Path, bodySize)
		return
	}

	sessionIDs := make([]uint64, 0, len(payload.SessionIDs))
	seen := make(map[uint64]struct{}, len(payload.SessionIDs))
	for _, rawID := range payload.SessionIDs {
		sessionID, err := strconv.ParseUint(rawID, 10, 64)
		if err != nil || sessionID == 0 {
			h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("invalid session id"), startTime, r.URL.Path, bodySize)
			return
		}
		if _, duplicate := seen[sessionID]; duplicate {
			h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, errors.New("duplicate session id"), startTime, r.URL.Path, bodySize)
			return
		}
		seen[sessionID] = struct{}{}

		exists, err := h.sessions.IsExists(projID, sessionID)
		if err != nil {
			h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusInternalServerError, err, startTime, r.URL.Path, bodySize)
			return
		}
		if !exists {
			h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusNotFound, errors.New("session not found"), startTime, r.URL.Path, bodySize)
			return
		}
		sessionIDs = append(sessionIDs, sessionID)
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set(
		"Content-Disposition",
		fmt.Sprintf("attachment; filename=\"openreplay-stitched-%d-%d.zip\"", sessionIDs[0], len(sessionIDs)),
	)
	w.Header().Set("Cache-Control", "private, no-store")

	tracker := &archiveWriteTracker{writer: w}
	if err := h.files.WriteStitchedSessionArchive(sessionIDs, tracker); err != nil {
		if !tracker.started {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Cache-Control")
			w.Header().Set("Content-Type", "application/json")
			status := http.StatusUnprocessableEntity
			if errors.Is(err, service.ErrSessionArchiveNotFound) {
				status = http.StatusNotFound
			}
			h.responser.ResponseWithError(h.log, r.Context(), w, status, err, startTime, r.URL.Path, bodySize)
			return
		}
		h.log.Error(r.Context(), "failed to stream stitched session archive after %d bytes: %v", tracker.written, err)
		panic(http.ErrAbortHandler)
	}
}

func (h *handlersImpl) getUnprocessedMob(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	bodySize := 0

	projID, err := api.GetProject(r)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, err, startTime, r.URL.Path, bodySize)
		return
	}

	sessID, err := api.GetSessionID(r)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, err, startTime, r.URL.Path, bodySize)
		return
	}

	notFoundResponse := map[string]interface{}{"errors": []string{"Replay file not found"}}

	isSessionExists, err := h.sessions.IsExists(projID, sessID)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusInternalServerError, err, startTime, r.URL.Path, bodySize)
		return
	}
	if !isSessionExists {
		h.responser.ResponseWithJSON(h.log, r.Context(), w, notFoundResponse, startTime, r.URL.Path, bodySize)
		return
	}
	var path string
	if r.URL.Query().Has("end") {
		path, err = h.files.GetUnprocessedMobE(sessID)
	} else {
		path, err = h.files.GetUnprocessedMob(sessID)
	}
	if err != nil {
		h.responser.ResponseWithJSON(h.log, r.Context(), w, notFoundResponse, startTime, r.URL.Path, bodySize)
		return
	}
	downloadHandler(w, r, path)
}

func downloadHandler(w http.ResponseWriter, r *http.Request, filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "File not found.", http.StatusNotFound)
		return
	}
	defer file.Close()

	fileName := filepath.Base(filePath)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fileName+"\"")

	http.ServeFile(w, r, filePath)
}

func (h *handlersImpl) getUnprocessedDevtools(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	bodySize := 0

	projID, err := api.GetProject(r)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, err, startTime, r.URL.Path, bodySize)
		return
	}

	sessID, err := api.GetSessionID(r)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusBadRequest, err, startTime, r.URL.Path, bodySize)
		return
	}

	notFoundResponse := map[string]interface{}{"errors": []string{"Devtools file not found"}}

	isSessionExists, err := h.sessions.IsExists(projID, sessID)
	if err != nil {
		h.responser.ResponseWithError(h.log, r.Context(), w, http.StatusInternalServerError, err, startTime, r.URL.Path, bodySize)
		return
	}
	if !isSessionExists {
		h.responser.ResponseWithJSON(h.log, r.Context(), w, notFoundResponse, startTime, r.URL.Path, bodySize)
		return
	}
	path, err := h.files.GetUnprocessedDevtools(sessID)
	if err != nil {
		h.responser.ResponseWithJSON(h.log, r.Context(), w, notFoundResponse, startTime, r.URL.Path, bodySize)
		return
	}
	downloadHandler(w, r, path)
}
