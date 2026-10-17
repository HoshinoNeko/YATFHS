package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HoshinoNeko/YATFHS/internal/observability"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

const chunkSize int64 = 4 << 20
const sessionTTL = 24 * time.Hour

type uploadSession struct {
	mu          sync.Mutex
	inactive    atomic.Bool
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	Expire      int       `json:"expire"`
	Fingerprint string    `json:"fingerprint"`
	IP          string    `json:"ip"`
	Admin       bool      `json:"admin"`
	Created     time.Time `json:"created_at"`
	CompletedID string    `json:"completed_id,omitempty"`
	Deleted     bool      `json:"-"`
}

func validID(id string) bool {
	if len(id) != 48 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func (h *Handler) sessionDir(id string) string { return filepath.Join(h.store.Dir(), ".uploads", id) }
func (h *Handler) saveSession(s *uploadSession) error {
	return storage.WriteJSON(filepath.Join(h.sessionDir(s.ID), "session.json"), s)
}
func (h *Handler) loadSessions() error {
	root := filepath.Join(h.store.Dir(), ".uploads")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "session.json"))
		var s uploadSession
		if err != nil || json.Unmarshal(b, &s) != nil || s.ID != e.Name() || s.Size < 0 || s.Size > h.cfg.MaxFileSizeBytes || s.Created.IsZero() || time.Since(s.Created) > sessionTTL {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
			continue
		}
		s.inactive.Store(s.CompletedID != "")
		h.sessions[s.ID] = &s
	}
	return nil
}
func (h *Handler) CreateUpload(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") || !h.allowUpload(w, r) {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		Expire      *int   `json:"expire"`
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 1024 || req.Size < 0 || req.Size > h.cfg.MaxFileSizeBytes || len(req.Fingerprint) > 256 {
		writeError(w, 400, "invalid filename, size or fingerprint")
		return
	}
	raw := ""
	if req.Expire != nil {
		raw = strconv.Itoa(*req.Expire)
	}
	admin := h.isAdmin(r)
	expire, err := h.expiry(raw, admin)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ip := h.limiter.RequestIP(r)
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	active := 0
	for _, s := range h.sessions {
		if s.IP == ip && !s.inactive.Load() && time.Since(s.Created) <= sessionTTL {
			active++
		}
	}
	if len(h.sessions) >= 1000 || active >= 10 {
		writeError(w, 429, "too many pending uploads; cancel old sessions or wait for cleanup")
		return
	}
	if !h.limiter.AllowUploadBytes(ip, req.Size) {
		writeError(w, 429, "hourly upload quota exceeded")
		return
	}
	id, err := storage.NewID()
	if err != nil {
		h.internalError(w, "failed to create upload", err)
		return
	}
	s := &uploadSession{ID: id, Name: req.Name, Size: req.Size, Expire: expire, Fingerprint: req.Fingerprint, IP: ip, Admin: admin, Created: time.Now()}
	if err = os.Mkdir(h.sessionDir(id), 0700); err == nil {
		err = h.saveSession(s)
	}
	if err != nil {
		_ = os.RemoveAll(h.sessionDir(id))
		h.internalError(w, "failed to persist upload session", err)
		return
	}
	h.sessions[id] = s
	h.sessionStatus(w, r, s)
}
func (s *uploadSession) count() int { return int((s.Size + chunkSize - 1) / chunkSize) }
func (s *uploadSession) expected(index int) int64 {
	return min(chunkSize, s.Size-int64(index)*chunkSize)
}
func (h *Handler) chunkPath(s *uploadSession, index int) string {
	return filepath.Join(h.sessionDir(s.ID), fmt.Sprintf("%d.part", index))
}
func (h *Handler) sessionStatus(w http.ResponseWriter, r *http.Request, s *uploadSession) {
	chunks := []int{}
	for i := 0; i < s.count(); i++ {
		if fi, err := os.Stat(h.chunkPath(s, i)); err == nil && fi.Size() == s.expected(i) {
			chunks = append(chunks, i)
		}
	}
	data := map[string]any{"id": s.ID, "name": s.Name, "size": s.Size, "expire": s.Expire, "fingerprint": s.Fingerprint, "chunk_size": chunkSize, "uploaded_chunks": chunks, "expires_at": s.Created.Add(sessionTTL)}
	if s.CompletedID != "" {
		if m, err := h.store.Get(s.CompletedID); err == nil {
			data["result"] = h.result(r, m)
		} else {
			data["result_expired"] = true
		}
	}
	ok(w, data)
}
func (h *Handler) UploadSession(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/uploads/"), "/")
	if len(parts) < 1 || len(parts) > 3 || !validID(parts[0]) {
		writeError(w, 404, "upload session not found")
		return
	}
	h.sessionsMu.Lock()
	s := h.sessions[parts[0]]
	h.sessionsMu.Unlock()
	if s == nil {
		writeError(w, 404, "upload session not found")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Deleted || time.Since(s.Created) > sessionTTL {
		writeError(w, 410, "upload session expired")
		return
	}
	if s.Admin && !h.isAdmin(r) {
		writeError(w, 401, "administrator authorization required")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			h.sessionStatus(w, r, s)
		case "DELETE":
			if err := os.RemoveAll(h.sessionDir(s.ID)); err != nil {
				h.internalError(w, "failed to cancel upload", err)
				return
			}
			s.Deleted = true
			s.inactive.Store(true)
			ok(w, map[string]bool{"cancelled": true})
		default:
			writeError(w, 405, "method not allowed")
		}
		return
	}
	if len(parts) == 2 && parts[1] == "complete" {
		if method(w, r, "POST") {
			h.completeUpload(w, r, s)
		}
		return
	}
	if len(parts) != 3 || parts[1] != "chunks" {
		writeError(w, 404, "not found")
		return
	}
	if !method(w, r, "PUT") {
		return
	}
	if s.CompletedID != "" {
		writeError(w, 409, "upload already completed")
		return
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 || index >= s.count() {
		writeError(w, 400, "invalid chunk index")
		return
	}
	expected := s.expected(index)
	r.Body = http.MaxBytesReader(w, r.Body, expected+1)
	tmp, err := os.CreateTemp(h.sessionDir(s.ID), ".part-*")
	if err != nil {
		h.internalError(w, "cannot write chunk", err)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, r.Body)
	closeErr := tmp.Close()
	if err != nil || n != expected {
		writeError(w, 400, "chunk size does not match expected size")
		return
	}
	if closeErr != nil {
		h.internalError(w, "cannot save chunk", closeErr)
		return
	}
	if err = os.Rename(tmp.Name(), h.chunkPath(s, index)); err != nil {
		h.internalError(w, "cannot commit chunk", err)
		return
	}
	ok(w, map[string]int{"index": index})
}
func (h *Handler) completeUpload(w http.ResponseWriter, r *http.Request, s *uploadSession) {
	if s.CompletedID != "" {
		if m, err := h.store.Get(s.CompletedID); err == nil {
			ok(w, h.result(r, m))
		} else {
			writeError(w, 410, "completed file expired or was deleted")
		}
		return
	}
	if _, err := h.expiry(strconv.Itoa(s.Expire), s.Admin); err != nil {
		writeError(w, 400, "expiry policy changed; start a new upload with an allowed expiry")
		return
	}
	for i := 0; i < s.count(); i++ {
		fi, err := os.Stat(h.chunkPath(s, i))
		if err != nil || fi.Size() != s.expected(i) {
			writeError(w, 409, "upload has missing chunks")
			return
		}
	}
	// Open one chunk at a time so large uploads do not exhaust file descriptors or memory.
	reader := &chunkReader{h: h, s: s}
	defer reader.Close()
	meta, err := h.store.SaveReader(reader, s.Name, s.Expire, s.IP)
	if err != nil {
		h.internalError(w, "failed to assemble upload", err)
		return
	}
	s.CompletedID = meta.ID
	if err = h.saveSession(s); err != nil {
		s.CompletedID = ""
		_ = h.store.Delete(meta.ID)
		h.internalError(w, "failed to persist completion", err)
		return
	}
	s.inactive.Store(true)
	for i := 0; i < s.count(); i++ {
		_ = os.Remove(h.chunkPath(s, i))
	}
	h.uploaded(meta)
	ok(w, h.result(r, meta))
}

type chunkReader struct {
	h       *Handler
	s       *uploadSession
	index   int
	current *os.File
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for c.index < c.s.count() {
		if c.current == nil {
			f, err := os.Open(c.h.chunkPath(c.s, c.index))
			if err != nil {
				return 0, err
			}
			c.current = f
		}
		n, err := c.current.Read(p)
		if err == io.EOF {
			c.current.Close()
			c.current = nil
			c.index++
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
	return 0, io.EOF
}
func (c *chunkReader) Close() error {
	if c.current != nil {
		return c.current.Close()
	}
	return nil
}
func (h *Handler) Cleanup() {
	n := h.store.DeleteExpired()
	if n > 0 {
		h.monitor.Record(observability.Event{Kind: "cleanup", Message: fmt.Sprintf("removed %d expired files", n)})
	}
	h.sessionsMu.Lock()
	sessions := make(map[string]*uploadSession, len(h.sessions))
	for id, s := range h.sessions {
		sessions[id] = s
	}
	h.sessionsMu.Unlock()
	for id, s := range sessions {
		s.mu.Lock()
		if s.Deleted || time.Since(s.Created) > sessionTTL {
			if os.RemoveAll(h.sessionDir(id)) == nil {
				s.Deleted = true
				s.inactive.Store(true)
				h.sessionsMu.Lock()
				delete(h.sessions, id)
				h.sessionsMu.Unlock()
			}
		}
		s.mu.Unlock()
	}
}
