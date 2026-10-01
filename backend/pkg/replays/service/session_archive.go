package service

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"

	"openreplay/backend/pkg/messages"
)

var ErrSessionArchiveNotFound = errors.New("session archive files not found")

type sessionArchiveObject struct {
	key  string
	name string
}

type sessionObjectLister interface {
	ListKeys(prefix string) ([]string, error)
}

type sessionArchiveManifest struct {
	Format    string   `json:"format"`
	Version   int      `json:"version"`
	SessionID string   `json:"sessionId"`
	Files     []string `json:"files"`
}

type stitchedSessionArchiveSegment struct {
	SessionID     string `json:"sessionId"`
	SourceStartTs uint64 `json:"sourceStartTs"`
	SourceEndTs   uint64 `json:"sourceEndTs"`
	TargetStartTs uint64 `json:"targetStartTs"`
	TargetEndTs   uint64 `json:"targetEndTs"`
	DurationMs    uint64 `json:"durationMs"`
}

type stitchedSessionArchiveManifest struct {
	Format           string                          `json:"format"`
	Version          int                             `json:"version"`
	SessionID        string                          `json:"sessionId"`
	SourceSessionIDs []string                        `json:"sourceSessionIds"`
	Files            []string                        `json:"files"`
	StartTs          uint64                          `json:"startTs"`
	EndTs            uint64                          `json:"endTs"`
	DurationMs       uint64                          `json:"durationMs"`
	Gaps             string                          `json:"gaps"`
	Segments         []stitchedSessionArchiveSegment `json:"segments"`
}

func (f *filesImpl) discoverSessionArchiveObjects(sessID uint64, sid string) ([]sessionArchiveObject, error) {
	prefix := sid + "/"
	if lister, ok := f.objStore.(sessionObjectLister); ok {
		keys, err := lister.ListKeys(prefix)
		if err != nil {
			return nil, fmt.Errorf("list session objects: %w", err)
		}
		files := make([]sessionArchiveObject, 0, len(keys))
		for _, key := range keys {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			rel := strings.TrimPrefix(key, prefix)
			if rel == "" || strings.HasSuffix(rel, "/") || strings.Contains(rel, "\\") {
				continue
			}
			clean := path.Clean(rel)
			if clean == "." || clean == ".." || clean != rel || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
				continue
			}
			files = append(files, sessionArchiveObject{
				key:  key,
				name: "raw/" + clean,
			})
		}
		sort.Slice(files, func(i, j int) bool {
			return files[i].name < files[j].name
		})
		return files, nil
	}

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
	sort.Slice(files, func(i, j int) bool {
		return files[i].name < files[j].name
	})
	return files, nil
}

func (f *filesImpl) WriteSessionArchive(sessID uint64, w io.Writer) error {
	sid := strconv.FormatUint(sessID, 10)
	files, err := f.discoverSessionArchiveObjects(sessID, sid)
	if err != nil {
		return err
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

	// Close finalizes the ZIP, so call it only after every object succeeds.
	// On error, leave the archive incomplete and let the HTTP handler fail
	// before the first write or abort an already-started response.
	archive := zip.NewWriter(w)
	manifestWriter, err := archive.CreateHeader(&zip.FileHeader{
		Name:   "manifest.json",
		Method: zip.Store,
	})
	if err != nil {
		return fmt.Errorf("create session archive manifest: %w", err)
	}
	if _, err := manifestWriter.Write(manifestBytes); err != nil {
		return fmt.Errorf("write session archive manifest: %w", err)
	}

	for _, file := range files {
		reader, err := f.objStore.Get(file.key)
		if err != nil {
			return fmt.Errorf("read session object %s: %w", file.key, err)
		}

		entry, err := archive.CreateHeader(&zip.FileHeader{
			Name:   file.name,
			Method: zip.Store,
		})
		if err != nil {
			_ = reader.Close()
			return fmt.Errorf("create archive entry %s: %w", file.name, err)
		}

		_, copyErr := io.Copy(entry, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			return fmt.Errorf("write archive entry %s: %w", file.name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close session object %s: %w", file.key, closeErr)
		}
	}

	if err := archive.Close(); err != nil {
		return fmt.Errorf("close session archive: %w", err)
	}
	return nil
}


func unpackSessionObject(data []byte) ([]byte, error) {
	if len(data) >= 3 && data[0] == 0x1f && data[1] == 0x8b && data[2] == 0x08 {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("open gzip replay object: %w", err)
		}
		defer reader.Close()
		unpacked, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read gzip replay object: %w", err)
		}
		return unpacked, nil
	}
	if len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd {
		reader, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("open zstd replay object: %w", err)
		}
		defer reader.Close()
		unpacked, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read zstd replay object: %w", err)
		}
		return unpacked, nil
	}
	return data, nil
}

func (f *filesImpl) readArchiveObject(object sessionArchiveObject) ([]byte, error) {
	reader, err := f.objStore.Get(object.key)
	if err != nil {
		return nil, fmt.Errorf("read session object %s: %w", object.key, err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read session object %s: %w", object.key, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close session object %s: %w", object.key, closeErr)
	}
	return unpackSessionObject(data)
}

func findArchiveObject(objects []sessionArchiveObject, name string) (sessionArchiveObject, bool) {
	for _, object := range objects {
		if object.name == name {
			return object, true
		}
	}
	return sessionArchiveObject{}, false
}

func (f *filesImpl) readSessionReplayStream(sessID uint64, names ...string) ([]byte, error) {
	objects, err := f.discoverSessionArchiveObjects(sessID, strconv.FormatUint(sessID, 10))
	if err != nil {
		return nil, err
	}
	parts := make([][]byte, 0, len(names))
	for _, name := range names {
		object, ok := findArchiveObject(objects, name)
		if !ok {
			continue
		}
		data, err := f.readArchiveObject(object)
		if err != nil {
			return nil, err
		}
		parts = append(parts, data)
	}
	if len(parts) == 0 {
		return nil, ErrSessionArchiveNotFound
	}
	return messages.JoinReplayContinuation(parts...)
}

func (f *filesImpl) WriteStitchedSessionArchive(sessIDs []uint64, w io.Writer) error {
	if len(sessIDs) == 0 {
		return ErrSessionArchiveNotFound
	}

	domStreams := make([][]byte, 0, len(sessIDs))
	devtoolsStreams := make([][]byte, 0, len(sessIDs))
	sourceIDs := make([]string, 0, len(sessIDs))
	hasDevtools := false

	for _, sessID := range sessIDs {
		dom, err := f.readSessionReplayStream(sessID, "raw/dom.mobs", "raw/dom.mobe")
		if err != nil {
			return fmt.Errorf("read replay session %d: %w", sessID, err)
		}
		domStreams = append(domStreams, dom)

		devtools, err := f.readSessionReplayStream(sessID, "raw/devtools.mob")
		if err != nil {
			if !errors.Is(err, ErrSessionArchiveNotFound) {
				return fmt.Errorf("read devtools session %d: %w", sessID, err)
			}
			devtoolsStreams = append(devtoolsStreams, nil)
		} else {
			hasDevtools = true
			devtoolsStreams = append(devtoolsStreams, devtools)
		}
		sourceIDs = append(sourceIDs, strconv.FormatUint(sessID, 10))
	}

	stitchedDOM, plan, err := messages.StitchReplayStreams(domStreams)
	if err != nil {
		return fmt.Errorf("stitch replay timeline: %w", err)
	}
	if len(plan) == 0 {
		return ErrSessionArchiveNotFound
	}

	files := []string{"raw/dom.mobs"}
	var stitchedDevtools []byte
	if hasDevtools {
		stitchedDevtools, err = messages.StitchReplayStreamsWithPlan(devtoolsStreams, plan)
		if err != nil {
			return fmt.Errorf("stitch devtools timeline: %w", err)
		}
		files = append(files, "raw/devtools.mob")
	}

	segments := make([]stitchedSessionArchiveSegment, len(plan))
	for i, segment := range plan {
		segments[i] = stitchedSessionArchiveSegment{
			SessionID:     sourceIDs[i],
			SourceStartTs: segment.SourceStart,
			SourceEndTs:   segment.SourceEnd,
			TargetStartTs: segment.TargetStart,
			TargetEndTs:   segment.TargetEnd,
			DurationMs:    segment.TargetEnd - segment.TargetStart,
		}
	}

	manifest := stitchedSessionArchiveManifest{
		Format:           "openreplay-stitched-session-export",
		Version:          1,
		SessionID:        fmt.Sprintf("stitched-%d-%d", sessIDs[0], len(sessIDs)),
		SourceSessionIDs: sourceIDs,
		Files:            files,
		StartTs:          plan[0].TargetStart,
		EndTs:            plan[len(plan)-1].TargetEnd,
		DurationMs:       plan[len(plan)-1].TargetEnd - plan[0].TargetStart,
		Gaps:             "compacted",
		Segments:         segments,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal stitched session manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	archive := zip.NewWriter(w)
	manifestWriter, err := archive.CreateHeader(&zip.FileHeader{
		Name:   "manifest.json",
		Method: zip.Store,
	})
	if err != nil {
		return fmt.Errorf("create stitched session manifest: %w", err)
	}
	if _, err := manifestWriter.Write(manifestBytes); err != nil {
		return fmt.Errorf("write stitched session manifest: %w", err)
	}

	domWriter, err := archive.CreateHeader(&zip.FileHeader{
		Name:   "raw/dom.mobs",
		Method: zip.Store,
	})
	if err != nil {
		return fmt.Errorf("create stitched DOM stream: %w", err)
	}
	if _, err := domWriter.Write(stitchedDOM); err != nil {
		return fmt.Errorf("write stitched DOM stream: %w", err)
	}

	if hasDevtools {
		devtoolsWriter, err := archive.CreateHeader(&zip.FileHeader{
			Name:   "raw/devtools.mob",
			Method: zip.Store,
		})
		if err != nil {
			return fmt.Errorf("create stitched devtools stream: %w", err)
		}
		if _, err := devtoolsWriter.Write(stitchedDevtools); err != nil {
			return fmt.Errorf("write stitched devtools stream: %w", err)
		}
	}

	if err := archive.Close(); err != nil {
		return fmt.Errorf("close stitched session archive: %w", err)
	}
	return nil
}
