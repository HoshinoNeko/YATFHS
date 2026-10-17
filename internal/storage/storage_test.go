package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPermanentLegacyAndExpiredCleanup(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	forever, err := s.SaveReader(strings.NewReader("metadata-safe"), "meta.json", 0, "ip")
	if err != nil {
		t.Fatal(err)
	}
	legacy := &FileMeta{ID: "oldLegacy123", OrigName: "legacy.txt", Size: 6, UploadedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	expired := &FileMeta{ID: "expired12345", OrigName: "old.txt", ExpiresAt: time.Now().Add(-time.Hour)}
	for _, m := range []*FileMeta{legacy, expired} {
		os.Mkdir(filepath.Join(dir, m.ID), 0700)
		os.WriteFile(filepath.Join(dir, m.ID, m.OrigName), []byte("legacy"), 0600)
		if err = WriteJSON(filepath.Join(dir, m.ID, "meta.json"), m); err != nil {
			t.Fatal(err)
		}
	}
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Stats(); n != 2 {
		t.Fatalf("active count %d", n)
	}
	if _, err = s.Get(expired.ID); !os.IsNotExist(err) {
		t.Fatal("expired file readable")
	}
	if s.DeleteExpired() != 1 {
		t.Fatal("expired files not cleaned after restart")
	}
	if _, err = os.Stat(filepath.Join(dir, expired.ID)); !os.IsNotExist(err) {
		t.Fatal("expired data retained")
	}
	if _, err = s.Get(forever.ID); err != nil {
		t.Fatal("permanent file expired")
	}
	b, _ := os.ReadFile(s.FilePath(forever.ID, forever.OrigName))
	if string(b) != "metadata-safe" {
		t.Fatal("meta.json filename overwrote metadata")
	}
	b, _ = os.ReadFile(s.FilePath(legacy.ID, legacy.OrigName))
	if string(b) != "legacy" {
		t.Fatal("legacy layout unreadable")
	}
}
