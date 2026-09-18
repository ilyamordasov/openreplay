package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var reviewFailure = errors.New("injected storage failure")

type reviewStore struct {
	*fakeObjectStorage
	listErr, getErr, readErr, closeErr error
	closed                             int
}

func (s *reviewStore) ListKeys(prefix string) ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.fakeObjectStorage.ListKeys(prefix)
}
func (s *reviewStore) Get(key string) (io.ReadCloser, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	data := s.objects[key]
	if s.readErr != nil {
		data = data[:len(data)/2]
	}
	return &reviewReader{reader: bytes.NewReader(data), owner: s}, nil
}

type reviewReader struct {
	reader *bytes.Reader
	owner  *reviewStore
}

func (r *reviewReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF && r.owner.readErr != nil {
		return n, r.owner.readErr
	}
	return n, err
}
func (r *reviewReader) Close() error { r.owner.closed++; return r.owner.closeErr }

// A returned storage error must never finalize a seemingly successful ZIP.
func TestReviewFailedArchiveMustNotBeValidZIP(t *testing.T) {
	for _, fault := range []string{"get", "read", "close"} {
		t.Run(fault, func(t *testing.T) {
			s := &reviewStore{fakeObjectStorage: &fakeObjectStorage{objects: map[string][]byte{"1/dom.mobs": bytes.Repeat([]byte("0123456789abcdef"), 1024)}}}
			switch fault {
			case "get":
				s.getErr = reviewFailure
			case "read":
				s.readErr = reviewFailure
			case "close":
				s.closeErr = reviewFailure
			}
			var output bytes.Buffer
			err := (&filesImpl{objStore: s}).WriteSessionArchive(1, &output)
			if !errors.Is(err, reviewFailure) {
				t.Fatalf("error=%v", err)
			}
			z, zerr := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
			if zerr == nil {
				// Also verify CRC of the short entry: this catches silent *valid* truncation.
				names := []string{}
				for _, f := range z.File {
					r, e := f.Open()
					if e != nil {
						t.Fatal(e)
					}
					b, e := io.ReadAll(r)
					r.Close()
					if e != nil {
						t.Fatal(e)
					}
					names = append(names, f.Name)
					t.Logf("accepted ZIP entry %s, %d bytes, CRC OK", f.Name, len(b))
				}
				t.Fatalf("storage failure finalized a valid ZIP: entries=%v, archiveBytes=%d", names, output.Len())
			}
			if fault != "get" && s.closed != 1 {
				t.Fatalf("reader closed %d times", s.closed)
			}
		})
	}
}
func TestReviewListingErrorWritesNothing(t *testing.T) {
	s := &reviewStore{fakeObjectStorage: &fakeObjectStorage{}, listErr: reviewFailure}
	var output bytes.Buffer
	if err := (&filesImpl{objStore: s}).WriteSessionArchive(1, &output); !errors.Is(err, reviewFailure) {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("wrote archive despite listing failure")
	}
}
func TestReviewBinaryAndManifest(t *testing.T) {
	data := make([]byte, 131072)
	for i := range data {
		data[i] = byte(i % 251)
	}
	store := &fakeObjectStorage{objects: map[string][]byte{"1/dom.mobs": data, "1/mobile/a.mob": {0, 255, 128, 10}, "1/canvas.tar.zst": {99, 0, 42}}}
	var output bytes.Buffer
	if err := (&filesImpl{objStore: store}).WriteSessionArchive(1, &output); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var manifest sessionArchiveManifest
	names := []string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "manifest.json" {
			if err := json.Unmarshal(b, &manifest); err != nil {
				t.Fatal(err)
			}
			continue
		}
		key := "1/" + strings.TrimPrefix(f.Name, "raw/")
		if !bytes.Equal(b, store.objects[key]) {
			t.Fatalf("content mismatch %s", key)
		}
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(manifest.Files, names) {
		t.Fatalf("manifest mismatch: %v / %v", manifest.Files, names)
	}
}
