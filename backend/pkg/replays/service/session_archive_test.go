package service

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"openreplay/backend/pkg/messages"
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


func makeStitchTestReplay(msgs ...messages.Message) []byte {
	data := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	var index [8]byte
	for i, msg := range msgs {
		binary.LittleEndian.PutUint64(index[:], uint64(i+1))
		data = append(data, index[:]...)
		data = append(data, msg.Encode()...)
	}
	return data
}

func TestWriteStitchedSessionArchiveCreatesOneCompactedReplay(t *testing.T) {
	store := &fakeObjectStorage{objects: map[string][]byte{
		"101/dom.mobs": makeStitchTestReplay(
			&messages.Timestamp{Timestamp: 1_000},
			&messages.SessionStart{Timestamp: 1_000, UserUUID: "fp-a"},
			&messages.CreateDocument{},
			&messages.Timestamp{Timestamp: 2_000},
			&messages.SessionEnd{Timestamp: 2_000},
		),
		"102/dom.mobs": makeStitchTestReplay(
			&messages.Timestamp{Timestamp: 10_000},
			&messages.SessionStart{Timestamp: 10_000, UserUUID: "fp-a"},
			&messages.CreateDocument{},
			&messages.Timestamp{Timestamp: 10_500},
			&messages.SessionEnd{Timestamp: 10_500},
		),
	}}
	files := &filesImpl{objStore: store}

	var output bytes.Buffer
	if err := files.WriteStitchedSessionArchive([]uint64{101, 102}, &output); err != nil {
		t.Fatalf("WriteStitchedSessionArchive() error = %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader() error = %v", err)
	}
	archiveFiles := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		archiveFiles[file.Name] = data
	}

	if _, ok := archiveFiles["raw/dom.mobs"]; !ok {
		t.Fatalf("stitched archive missing raw/dom.mobs")
	}
	if len(archiveFiles) != 2 {
		t.Fatalf("archive files = %v, expected only manifest + one stitched replay stream", archiveFiles)
	}

	var manifest stitchedSessionArchiveManifest
	if err := json.Unmarshal(archiveFiles["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != "openreplay-stitched-session-export" || manifest.Gaps != "preserved" {
		t.Fatalf("unexpected stitched manifest: %#v", manifest)
	}
	if len(manifest.SourceSessionIDs) != 2 || manifest.SourceSessionIDs[0] != "101" || manifest.SourceSessionIDs[1] != "102" {
		t.Fatalf("source session ids = %#v", manifest.SourceSessionIDs)
	}
	if manifest.StartTs != 1_000 || manifest.EndTs != 10_500 || manifest.DurationMs != 9_500 {
		t.Fatalf("stitched timeline = start:%d end:%d duration:%d", manifest.StartTs, manifest.EndTs, manifest.DurationMs)
	}
	if len(manifest.Segments) != 2 {
		t.Fatalf("segments = %#v, want 2", manifest.Segments)
	}
	if manifest.Segments[0].SessionID != "101" ||
		manifest.Segments[0].SourceStartTs != 1_000 ||
		manifest.Segments[0].TargetStartTs != 1_000 ||
		manifest.Segments[0].TargetEndTs != 2_000 {
		t.Fatalf("first segment = %#v", manifest.Segments[0])
	}
	if manifest.Segments[1].SessionID != "102" ||
		manifest.Segments[1].SourceStartTs != 10_000 ||
		manifest.Segments[1].TargetStartTs != 10_000 ||
		manifest.Segments[1].TargetEndTs != 10_500 {
		t.Fatalf("second segment = %#v", manifest.Segments[1])
	}

	decoded, err := messages.DecodeReplayStream(archiveFiles["raw/dom.mobs"])
	if err != nil {
		t.Fatalf("decode stitched DOM: %v", err)
	}
	var timestamps []uint64
	for _, msg := range decoded {
		if ts, ok := msg.(*messages.Timestamp); ok {
			timestamps = append(timestamps, ts.Timestamp)
		}
	}
	want := []uint64{1_000, 2_000, 10_000, 10_500}
	if len(timestamps) != len(want) {
		t.Fatalf("timestamps = %#v", timestamps)
	}
	for i := range want {
		if timestamps[i] != want[i] {
			t.Fatalf("timestamps = %#v, want %#v", timestamps, want)
		}
	}
}
