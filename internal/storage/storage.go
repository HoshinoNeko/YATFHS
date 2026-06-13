package storage

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const idChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const idLen = 12

type FileMeta struct {
	ID          string    `json:"id"`
	OrigName    string    `json:"orig_name"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	UploadedAt  time.Time `json:"uploaded_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	UploaderIP  string    `json:"uploader_ip"`
}

func (m *FileMeta) IsExpired() bool {
	return time.Now().After(m.ExpiresAt)
}

type Store struct {
	dir  string
	mu   sync.RWMutex
	meta map[string]*FileMeta // id -> meta
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	s := &Store{
		dir:  dir,
		meta: make(map[string]*FileMeta),
	}
	if err := s.loadMeta(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Save(file multipart.File, header *multipart.FileHeader, expireSec int, uploaderIP string) (*FileMeta, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	// Sanitize filename
	origName := sanitizeFilename(header.Filename)

	// Detect content type
	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	ct := http.DetectContentType(buf[:n])
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Create directory for this file
	fileDir := filepath.Join(s.dir, id)
	if err := os.MkdirAll(fileDir, 0755); err != nil {
		return nil, fmt.Errorf("create file dir: %w", err)
	}

	// Write file
	dst, err := os.Create(filepath.Join(fileDir, origName))
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	defer dst.Close()

	size, err := io.Copy(dst, file)
	if err != nil {
		os.RemoveAll(fileDir)
		return nil, fmt.Errorf("write file: %w", err)
	}

	now := time.Now()
	m := &FileMeta{
		ID:          id,
		OrigName:    origName,
		Size:        size,
		ContentType: ct,
		UploadedAt:  now,
		ExpiresAt:   now.Add(time.Duration(expireSec) * time.Second),
		UploaderIP:  uploaderIP,
	}

	// Persist metadata
	if err := s.saveMeta(m); err != nil {
		os.RemoveAll(fileDir)
		return nil, err
	}

	s.mu.Lock()
	s.meta[id] = m
	s.mu.Unlock()

	return m, nil
}

func (s *Store) Get(id string) (*FileMeta, error) {
	s.mu.RLock()
	m, ok := s.meta[id]
	s.mu.RUnlock()

	if !ok {
		return nil, os.ErrNotExist
	}
	if m.IsExpired() {
		return nil, os.ErrNotExist
	}
	return m, nil
}

func (s *Store) FilePath(id, name string) string {
	return filepath.Join(s.dir, id, name)
}

func (s *Store) DeleteExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var toDelete []string
	for id, m := range s.meta {
		if m.IsExpired() {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		delete(s.meta, id)
		os.RemoveAll(filepath.Join(s.dir, id))
	}
	return len(toDelete)
}

func (s *Store) Stats() (totalFiles int, totalBytes int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.meta {
		if !m.IsExpired() {
			totalFiles++
			totalBytes += m.Size
		}
	}
	return
}

// --- internal ---

func (s *Store) saveMeta(m *FileMeta) error {
	metaPath := filepath.Join(s.dir, m.ID, "meta.json")
	f, err := os.Create(metaPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(m)
}

func (s *Store) loadMeta() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil // empty dir is fine
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(s.dir, e.Name(), "meta.json")
		f, err := os.Open(metaPath)
		if err != nil {
			continue
		}
		var m FileMeta
		if err := json.NewDecoder(f).Decode(&m); err != nil {
			f.Close()
			continue
		}
		f.Close()
		if !m.IsExpired() {
			s.meta[m.ID] = &m
		}
	}
	return nil
}

func generateID() (string, error) {
	b := make([]byte, idLen)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(idChars))))
		if err != nil {
			return "", err
		}
		b[i] = idChars[n.Int64()]
	}
	return string(b), nil
}

func sanitizeFilename(name string) string {
	// Keep only the base name
	name = filepath.Base(name)
	// Replace any path separators
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	// Trim spaces
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return "file"
	}
	return name
}
