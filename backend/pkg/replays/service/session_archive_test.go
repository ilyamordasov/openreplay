package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"openreplay/backend/pkg/objectstorage"
)

type fakeObjectStorage struct {
	objects map[string][]byte
}

func (s *fakeObjectStorage) Upload(io.Reader, string, string, string, objectstorage.CompressionType) error {
	return nil
}

func (s *fakeObjectStorage) Get(key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, ErrSessionArchiveNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeObjectStorage) Exists(key string) bool {
	_, ok := s.objects[key]
	return ok
}

func (s *fakeObjectStorage) ListKeys(prefix string) ([]string, error) {
	keys := make([]string, 0)
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *fakeObjectStorage) GetCreationTime(string) *time.Time { return nil }
func (s *fakeObjectStorage) GetPreSignedUploadUrl(string) (string, error) {
	return "", nil
}
func (s *fakeObjectStorage) GetPreSignedDownloadUrl(string) (string, error) {
	return "", nil
}
func (s *fakeObjectStorage) GetPreSignedDownloadUrlFromBucket(string, string) (string, error) {
	return "", nil
}
func (s *fakeObjectStorage) Tag(string, string, string) error { return nil }

func TestWriteSessionArchive(t *testing.T) {
	sessionID := uint64(4020541843067130369)
	sid := strconv.FormatUint(sessionID, 10)
	store := &fakeObjectStorage{objects: map[string][]byte{
		sid + "/dom.mobs":                  []byte("dom-start"),
		sid + "/dom.mobe":                  []byte("dom-end"),
		sid + "/devtools.mob":              []byte("devtools"),
		sid + "/mobile/segment-0001.mob":    []byte("mobile-segment"),
		sid + "/nested/../../escape.secret": []byte("must-not-export"),
		"999/mobile/other.mob":              []byte("other-session"),
	}}
	files := &filesImpl{objStore: store}

	var output bytes.Buffer
	if err := files.WriteSessionArchive(sessionID, &output); err != nil {
		t.Fatalf("WriteSessionArchive() error = %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader() error = %v", err)
	}

	archiveFiles := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		archiveFiles[file.Name] = data
	}

	for name, expected := range map[string]string{
		"raw/dom.mobs":                "dom-start",
		"raw/dom.mobe":                "dom-end",
		"raw/devtools.mob":            "devtools",
		"raw/mobile/segment-0001.mob": "mobile-segment",
	} {
		if got := string(archiveFiles[name]); got != expected {
			t.Fatalf("%s = %q, want %q", name, got, expected)
		}
	}
	if _, ok := archiveFiles["raw/mobile/other.mob"]; ok {
		t.Fatalf("archive contains an object from another session")
	}
	for name := range archiveFiles {
		if strings.Contains(name, "..") || name == "escape.secret" || strings.HasSuffix(name, "/escape.secret") {
			t.Fatalf("archive contains unsafe path %q", name)
		}
	}

	var manifest sessionArchiveManifest
	if err := json.Unmarshal(archiveFiles["manifest.json"], &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.Format != "openreplay-session-export" {
		t.Fatalf("manifest format = %q", manifest.Format)
	}
	if manifest.Version != 1 {
		t.Fatalf("manifest version = %d", manifest.Version)
	}
	if manifest.SessionID != sid {
		t.Fatalf("manifest sessionId = %q, want %q", manifest.SessionID, sid)
	}
	if len(manifest.Files) != 4 {
		t.Fatalf("manifest files = %v", manifest.Files)
	}
}

func TestWriteSessionArchiveNotFound(t *testing.T) {
	files := &filesImpl{objStore: &fakeObjectStorage{objects: map[string][]byte{}}}

	var output bytes.Buffer
	err := files.WriteSessionArchive(1, &output)
	if err != ErrSessionArchiveNotFound {
		t.Fatalf("WriteSessionArchive() error = %v, want %v", err, ErrSessionArchiveNotFound)
	}
	if output.Len() != 0 {
		t.Fatalf("archive should not write bytes when no session objects exist")
	}
}
