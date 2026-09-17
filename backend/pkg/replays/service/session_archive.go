package service

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

var ErrSessionArchiveNotFound = errors.New("session archive files not found")

type sessionArchiveObject struct {
	key  string
	name string
}

type sessionArchiveManifest struct {
	Format    string   `json:"format"`
	Version   int      `json:"version"`
	SessionID string   `json:"sessionId"`
	Files     []string `json:"files"`
}

func (f *filesImpl) WriteSessionArchive(sessID uint64, w io.Writer) error {
	sid := strconv.FormatUint(sessID, 10)
	candidates := []sessionArchiveObject{
		{key: sid + "/dom.mobs", name: "raw/dom.mobs"},
		{key: sid + "/dom.mobe", name: "raw/dom.mobe"},
		{key: sid + "/devtools.mob", name: "raw/devtools.mob"},
		{key: sid + "/replay.frames.zst", name: "raw/replay.frames.zst"},
		{key: sid + "/replay.tar.zst", name: "raw/replay.tar.zst"},
	}

	if f.canvases != nil {
		if recIDs, err := f.canvases.Get(sessID); err == nil {
			for _, recID := range recIDs {
				candidates = append(candidates,
					sessionArchiveObject{
						key:  fmt.Sprintf("%d/%s.webp.frames.zst", sessID, recID),
						name: fmt.Sprintf("raw/canvas/%s.webp.frames.zst", recID),
					},
					sessionArchiveObject{
						key:  fmt.Sprintf("%d/%s.tar.zst", sessID, recID),
						name: fmt.Sprintf("raw/canvas/%s.tar.zst", recID),
					},
				)
			}
		}
	}

	files := make([]sessionArchiveObject, 0, len(candidates))
	for _, candidate := range candidates {
		if f.objStore.Exists(candidate.key) {
			files = append(files, candidate)
		}
	}
	if len(files) == 0 {
		return ErrSessionArchiveNotFound
	}

	manifestFiles := make([]string, 0, len(files))
	for _, file := range files {
		manifestFiles = append(manifestFiles, file.name)
	}
	manifestBytes, err := json.MarshalIndent(sessionArchiveManifest{
		Format:    "openreplay-session-export",
		Version:   1,
		SessionID: sid,
		Files:     manifestFiles,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session archive manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	archive := zip.NewWriter(w)
	manifestWriter, err := archive.CreateHeader(&zip.FileHeader{
		Name:   "manifest.json",
		Method: zip.Store,
	})
	if err != nil {
		_ = archive.Close()
		return fmt.Errorf("create session archive manifest: %w", err)
	}
	if _, err := manifestWriter.Write(manifestBytes); err != nil {
		_ = archive.Close()
		return fmt.Errorf("write session archive manifest: %w", err)
	}

	for _, file := range files {
		reader, err := f.objStore.Get(file.key)
		if err != nil {
			_ = archive.Close()
			return fmt.Errorf("read session object %s: %w", file.key, err)
		}

		entry, err := archive.CreateHeader(&zip.FileHeader{
			Name:   file.name,
			Method: zip.Store,
		})
		if err != nil {
			_ = reader.Close()
			_ = archive.Close()
			return fmt.Errorf("create archive entry %s: %w", file.name, err)
		}

		_, copyErr := io.Copy(entry, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			_ = archive.Close()
			return fmt.Errorf("write archive entry %s: %w", file.name, copyErr)
		}
		if closeErr != nil {
			_ = archive.Close()
			return fmt.Errorf("close session object %s: %w", file.key, closeErr)
		}
	}

	if err := archive.Close(); err != nil {
		return fmt.Errorf("close session archive: %w", err)
	}
	return nil
}
