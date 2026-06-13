package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"github.com/HoshinoNeko/YATFHS/internal/config"
	"github.com/HoshinoNeko/YATFHS/internal/middleware"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

type Handler struct {
	cfg     *config.Config
	store   *storage.Store
	limiter *middleware.Limiter
}

func New(cfg *config.Config, store *storage.Store, limiter *middleware.Limiter) *Handler {
	return &Handler{cfg: cfg, store: store, limiter: limiter}
}

// --- response helpers ---

type apiResponse struct {
	Status  string      `json:"status"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
}

type uploadData struct {
	URL string `json:"url"`
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiResponse{Status: "error", Message: msg})
}

// --- Upload ---

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ip := middleware.ExtractIP(r)

	// Rate limit: request count
	if !h.limiter.AllowUpload(ip) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "upload rate limit exceeded, please wait")
		return
	}

	// Parse multipart (limit to MaxFileSize + 1 MB for fields)
	maxParse := h.cfg.MaxFileSizeBytes + 1*1024*1024
	if err := r.ParseMultipartForm(maxParse); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'file' field")
		return
	}
	defer file.Close()

	// File size check
	if header.Size > h.cfg.MaxFileSizeBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds max size of %d MB", h.cfg.MaxFileSizeBytes/1024/1024))
		return
	}

	// Byte quota rate limit
	if !h.limiter.AllowUploadBytes(ip, header.Size) {
		writeError(w, http.StatusTooManyRequests, "hourly upload quota exceeded")
		return
	}

	// Parse expire
	expireStr := r.FormValue("expire")
	expire := h.cfg.DefaultExpireSec
	if expireStr != "" {
		v, err := strconv.Atoi(expireStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "expire must be an integer (seconds)")
			return
		}
		if v < h.cfg.MinExpireSec || v > h.cfg.MaxExpireSec {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("expire must be between %d and %d seconds", h.cfg.MinExpireSec, h.cfg.MaxExpireSec))
			return
		}
		expire = v
	}

	meta, err := h.store.Save(file, header, expire, ip)
	if err != nil {
		log.Printf("[upload] error saving file: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to save file")
		return
	}

	url := fmt.Sprintf("%s/%s/%s", strings.TrimRight(h.cfg.BaseURL, "/"), meta.ID, meta.OrigName)
	log.Printf("[upload] %s uploaded %s (%d bytes, expire %ds) -> %s", ip, meta.OrigName, meta.Size, expire, meta.ID)

	writeJSON(w, http.StatusOK, apiResponse{
		Status: "success",
		Data:   uploadData{URL: url},
	})
}

// --- Download ---

func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ip := middleware.ExtractIP(r)
	if !h.limiter.AllowDownload(ip) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "download rate limit exceeded")
		return
	}

	// Path: /{id}/{filename}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	id, filename := parts[0], parts[1]

	meta, err := h.store.Get(id)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}

	// Verify filename matches (security: prevent path traversal)
	if filepath.Base(filename) != meta.OrigName {
		http.NotFound(w, r)
		return
	}

	filePath := h.store.FilePath(id, meta.OrigName)
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, meta.OrigName))
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("X-Expires-At", meta.ExpiresAt.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "no-store")

	log.Printf("[download] %s downloading %s/%s", ip, id, meta.OrigName)
	http.ServeContent(w, r, meta.OrigName, meta.UploadedAt, f)
}

// --- Stats (public) ---

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	files, bytes := h.store.Stats()
	writeJSON(w, http.StatusOK, apiResponse{
		Status: "success",
		Data: map[string]interface{}{
			"active_files":  files,
			"total_bytes":   bytes,
			"max_file_size": h.cfg.MaxFileSizeBytes,
			"limits": map[string]interface{}{
				"min_expire_sec": h.cfg.MinExpireSec,
				"max_expire_sec": h.cfg.MaxExpireSec,
			},
		},
	})
}

// --- Index (serves embedded HTML) ---

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(indexHTML))
}
