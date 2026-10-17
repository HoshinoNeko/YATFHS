package handler

import (
	"archive/zip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HoshinoNeko/YATFHS/internal/config"
	"github.com/HoshinoNeko/YATFHS/internal/middleware"
	"github.com/HoshinoNeko/YATFHS/internal/observability"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

type Handler struct {
	cfg        *config.Config
	store      *storage.Store
	limiter    *middleware.Limiter
	monitor    *observability.Monitor
	policyMu   sync.RWMutex
	policy     config.ExpiryPolicy
	sessionsMu sync.Mutex
	sessions   map[string]*uploadSession
}

func New(cfg *config.Config, store *storage.Store, limiter *middleware.Limiter) (*Handler, error) {
	m, err := observability.New(store.Dir())
	if err != nil {
		return nil, err
	}
	h := &Handler{cfg: cfg, store: store, limiter: limiter, monitor: m, policy: cfg.Policy(), sessions: map[string]*uploadSession{}}
	b, err := os.ReadFile(filepath.Join(store.Dir(), "settings.json"))
	if err == nil {
		if err = json.Unmarshal(b, &h.policy); err != nil {
			return nil, err
		}
		if err = h.policy.Validate(); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = h.loadSessions(); err != nil {
		return nil, err
	}
	return h, nil
}
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			h.Index(w, r)
		} else {
			h.Download(w, r)
		}
	})
	mux.HandleFunc("/assets/", h.Asset)
	mux.HandleFunc("/api/v1/upload", h.Upload)
	mux.HandleFunc("/api/v1/uploads", h.CreateUpload)
	mux.HandleFunc("/api/v1/uploads/", h.UploadSession)
	mux.HandleFunc("/api/v1/stats", h.Stats)
	mux.HandleFunc("/api/v1/downloads", h.Archive)
	mux.HandleFunc("/admin", h.AdminPage)
	mux.HandleFunc("/api/v1/admin/", h.Admin)
	return h.monitor.Wrap(mux, h.limiter.RequestIP)
}

type apiResponse struct {
	Status  string `json:"status"`
	Data    any    `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
}
type uploadData struct {
	URL       string     `json:"url"`
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiResponse{Status: "error", Message: msg})
}
func (h *Handler) internalError(w http.ResponseWriter, message string, err error) {
	h.monitor.Record(observability.Event{Kind: "error", Status: 500, Message: message + ": " + err.Error()})
	log.Printf("[error] %s: %v", message, err)
	writeError(w, 500, message)
}
func ok(w http.ResponseWriter, v any) { writeJSON(w, 200, apiResponse{Status: "success", Data: v}) }
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want {
		w.Header().Set("Allow", want)
		writeError(w, 405, "method not allowed")
		return false
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "expected one JSON object")
		return false
	}
	return true
}
func (h *Handler) isAdmin(r *http.Request) bool {
	if h.cfg.AdminToken == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	a := sha256.Sum256([]byte(token))
	b := sha256.Sum256([]byte(h.cfg.AdminToken))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
func (h *Handler) getPolicy() config.ExpiryPolicy {
	h.policyMu.RLock()
	defer h.policyMu.RUnlock()
	p := h.policy
	p.ExpireOptionsSec = append([]int(nil), p.ExpireOptionsSec...)
	return p
}
func (h *Handler) expiry(raw string, admin bool) (int, error) {
	p := h.getPolicy()
	n := p.DefaultExpireSec
	if raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("expire must be an integer in seconds")
		}
		n = v
	}
	if admin {
		if n < 0 || n > config.MaxExpireSec {
			return 0, fmt.Errorf("admin expiry must be 0 (never) or 1–%d seconds", config.MaxExpireSec)
		}
	} else if n < p.MinExpireSec || n > p.MaxExpireSec {
		return 0, fmt.Errorf("expire must be between %d and %d seconds", p.MinExpireSec, p.MaxExpireSec)
	}
	return n, nil
}
func (h *Handler) baseURL(r *http.Request) string {
	if h.cfg.BaseURL != "" {
		return h.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if h.limiter.IsTrustedProxy(middleware.ExtractIP(r)) {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}
func (h *Handler) result(r *http.Request, m *storage.FileMeta) uploadData {
	var expires *time.Time
	if !m.ExpiresAt.IsZero() {
		t := m.ExpiresAt
		expires = &t
	}
	return uploadData{h.baseURL(r) + "/" + url.PathEscape(m.ID) + "/" + url.PathEscape(m.OrigName), m.ID, m.OrigName, m.Size, expires}
}
func (h *Handler) uploaded(m *storage.FileMeta) {
	h.monitor.Record(observability.Event{Kind: "upload", IP: m.UploaderIP, Bytes: m.Size, Message: m.ID + " " + m.OrigName})
}
func (h *Handler) allowUpload(w http.ResponseWriter, r *http.Request) bool {
	if !h.limiter.AllowUpload(h.limiter.RequestIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "upload rate limit exceeded")
		return false
	}
	return true
}
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") || !h.allowUpload(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxFileSizeBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		code := 400
		if errors.As(err, &tooLarge) {
			code = 413
		}
		writeError(w, code, "invalid or oversized multipart upload")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "missing 'file' field")
		return
	}
	defer file.Close()
	if header.Size > h.cfg.MaxFileSizeBytes {
		writeError(w, 413, "file exceeds configured maximum size")
		return
	}
	expire, err := h.expiry(r.FormValue("expire"), h.isAdmin(r))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ip := h.limiter.RequestIP(r)
	if !h.limiter.AllowUploadBytes(ip, header.Size) {
		writeError(w, 429, "hourly upload quota exceeded")
		return
	}
	meta, err := h.store.Save(file, header, expire, ip)
	if err != nil {
		h.internalError(w, "failed to save file", err)
		return
	}
	h.uploaded(meta)
	ok(w, h.result(r, meta))
}
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		writeError(w, 405, "method not allowed")
		return
	}
	if !h.limiter.AllowDownload(h.limiter.RequestIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "download rate limit exceeded")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	m, err := h.store.Get(parts[0])
	if err != nil || parts[1] != m.OrigName {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(h.store.FilePath(m.ID, m.OrigName))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": m.OrigName}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if !m.ExpiresAt.IsZero() {
		w.Header().Set("X-Expires-At", m.ExpiresAt.UTC().Format(http.TimeFormat))
	}
	cw := &countWriter{ResponseWriter: w}
	http.ServeContent(cw, r, m.OrigName, m.UploadedAt, f)
	if r.Method == "GET" && cw.code >= 200 && cw.code < 300 {
		h.monitor.Record(observability.Event{Kind: "download", IP: h.limiter.RequestIP(r), Message: m.ID, Bytes: cw.n})
	}
}
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	n, b := h.store.Stats()
	p := h.getPolicy()
	ok(w, map[string]any{"active_files": n, "total_bytes": b, "max_file_size": h.cfg.MaxFileSizeBytes, "limits": p, "chunk_size": chunkSize, "session_ttl_seconds": int(sessionTTL.Seconds())})
}
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, indexHTML)
}
func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			writeError(w, 400, "invalid download form")
			return
		}
		if err := json.Unmarshal([]byte(r.PostForm.Get("ids")), &req.IDs); err != nil {
			writeError(w, 400, "invalid file IDs")
			return
		}
	} else if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) < 1 || len(req.IDs) > 100 {
		writeError(w, 400, "select between 1 and 100 files")
		return
	}
	type entry struct {
		m *storage.FileMeta
		f *os.File
	}
	entries := []entry{}
	defer func() {
		for _, e := range entries {
			e.f.Close()
		}
	}()
	seen := map[string]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if !h.limiter.AllowDownload(h.limiter.RequestIP(r)) {
			writeError(w, 429, "download rate limit exceeded")
			return
		}
		m, err := h.store.Get(id)
		if err != nil {
			writeError(w, 404, "file missing or expired: "+id)
			return
		}
		f, err := os.Open(h.store.FilePath(id, m.OrigName))
		if err != nil {
			writeError(w, 404, "file unavailable")
			return
		}
		entries = append(entries, entry{m, f})
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="yatfhs-files.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	names := map[string]int{}
	for _, e := range entries {
		name := e.m.OrigName
		for names[name] > 0 {
			name = e.m.ID + "-" + name
		}
		names[name]++
		out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: e.m.UploadedAt})
		if err != nil {
			return
		}
		n, err := io.Copy(out, e.f)
		if err != nil {
			return
		}
		h.monitor.Record(observability.Event{Kind: "download", IP: h.limiter.RequestIP(r), Message: e.m.ID, Bytes: n})
	}
	if err := zw.Close(); err != nil {
		log.Printf("[archive] %v", err)
	}
}

type countWriter struct {
	http.ResponseWriter
	code int
	n    int64
}

func (w *countWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *countWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}
