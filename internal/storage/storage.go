package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type FileMeta struct {
	ID          string    `json:"id"`
	OrigName    string    `json:"orig_name"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	UploadedAt  time.Time `json:"uploaded_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	UploaderIP  string    `json:"uploader_ip"`
	StorageName string    `json:"storage_name,omitempty"`
}

func (m *FileMeta) IsExpired() bool { return !m.ExpiresAt.IsZero() && !time.Now().Before(m.ExpiresAt) }

type Store struct {
	dir  string
	mu   sync.RWMutex
	meta map[string]*FileMeta
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, meta: map[string]*FileMeta{}}
	if err := s.loadMeta(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) Dir() string { return s.dir }
func (s *Store) Save(file multipart.File, header *multipart.FileHeader, expire int, ip string) (*FileMeta, error) {
	return s.SaveReader(file, header.Filename, expire, ip)
}
func (s *Store) SaveReader(src io.Reader, name string, expire int, ip string) (*FileMeta, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dir, id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(dir)
		}
	}()
	dst, err := os.OpenFile(filepath.Join(dir, "content"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	size, err := io.Copy(dst, src)
	if err != nil {
		dst.Close()
		return nil, err
	}
	if _, err = dst.Seek(0, io.SeekStart); err != nil {
		dst.Close()
		return nil, err
	}
	buf := make([]byte, 512)
	n, readErr := dst.Read(buf)
	if readErr != nil && readErr != io.EOF {
		dst.Close()
		return nil, readErr
	}
	if err = dst.Close(); err != nil {
		return nil, err
	}
	now := time.Now()
	m := &FileMeta{ID: id, OrigName: sanitizeFilename(name), Size: size, ContentType: http.DetectContentType(buf[:n]), UploadedAt: now, UploaderIP: ip, StorageName: "content"}
	if expire > 0 {
		m.ExpiresAt = now.Add(time.Duration(expire) * time.Second)
	}
	if err = WriteJSON(filepath.Join(dir, "meta.json"), m); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.meta[id] = m
	s.mu.Unlock()
	success = true
	return m, nil
}
func (s *Store) Get(id string) (*FileMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.meta[id]
	if !ok || m.IsExpired() {
		return nil, os.ErrNotExist
	}
	return m, nil
}
func (s *Store) FilePath(id, name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m := s.meta[id]; m != nil && m.StorageName != "" {
		name = m.StorageName
	}
	return filepath.Join(s.dir, id, name)
}
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.meta[id]; !ok {
		return os.ErrNotExist
	}
	if err := os.RemoveAll(filepath.Join(s.dir, id)); err != nil {
		return err
	}
	delete(s.meta, id)
	return nil
}
func (s *Store) DeleteExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, m := range s.meta {
		if m.IsExpired() {
			if os.RemoveAll(filepath.Join(s.dir, id)) == nil {
				delete(s.meta, id)
				n++
			}
		}
	}
	return n
}
func (s *Store) Stats() (int, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	var size int64
	for _, m := range s.meta {
		if !m.IsExpired() {
			n++
			size += m.Size
		}
	}
	return n, size
}
func (s *Store) List() []FileMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []FileMeta{}
	for _, m := range s.meta {
		if !m.IsExpired() {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UploadedAt.After(out[j].UploadedAt) })
	return out
}
func (s *Store) loadMeta() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var m FileMeta
		if json.Unmarshal(b, &m) != nil || m.ID != e.Name() || m.OrigName != sanitizeFilename(m.OrigName) || (m.StorageName != "" && m.StorageName != "content") {
			continue
		}
		// Keep expired entries so cleanup removes files left over across a restart.
		s.meta[m.ID] = &m
	}
	return nil
}
func NewID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func WriteJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if len(name) > 240 {
		return fmt.Sprintf("file%s", filepath.Ext(name)[:min(len(filepath.Ext(name)), 20)])
	}
	return name
}
