package handler

import (
	_ "embed"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/HoshinoNeko/YATFHS/internal/config"
	"github.com/HoshinoNeko/YATFHS/internal/observability"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

//go:embed admin.html
var adminHTML string

func (h *Handler) AdminPage(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = io.WriteString(w, adminHTML)
}
func (h *Handler) Admin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AdminToken == "" {
		writeError(w, 503, "admin is disabled; configure YATFHS_ADMIN_TOKEN and restart")
		return
	}
	if !h.isAdmin(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="YATFHS admin"`)
		writeError(w, 401, "invalid administrator token")
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/api/v1/admin/") {
	case "overview":
		if !method(w, r, "GET") {
			return
		}
		stats, events := h.monitor.Snapshot()
		n, b := h.store.Stats()
		stats["active_files"] = n
		stats["stored_bytes"] = b
		stats["go_version"] = runtime.Version()
		stats["goroutines"] = runtime.NumGoroutine()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		stats["memory_bytes"] = mem.Alloc
		ok(w, map[string]any{"stats": stats, "logs": events, "policy": h.getPolicy(), "max_file_size": h.cfg.MaxFileSizeBytes})
	case "files":
		if !method(w, r, "GET") {
			return
		}
		files := h.store.List()
		out := make([]map[string]any, 0, len(files))
		for _, m := range files {
			out = append(out, map[string]any{"file": m, "download": h.result(r, &m)})
		}
		ok(w, out)
	case "settings":
		if r.Method == "GET" {
			ok(w, h.getPolicy())
			return
		}
		if !method(w, r, "PUT") {
			return
		}
		var p config.ExpiryPolicy
		if !decode(w, r, &p) {
			return
		}
		if err := p.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		h.policyMu.Lock()
		err := storage.WriteJSON(filepath.Join(h.store.Dir(), "settings.json"), p)
		if err == nil {
			h.policy = p
		}
		h.policyMu.Unlock()
		if err != nil {
			h.internalError(w, "failed to persist settings", err)
			return
		}
		h.monitor.Record(observability.Event{Kind: "admin", IP: h.limiter.RequestIP(r), Message: "updated expiry policy"})
		ok(w, p)
	case "cleanup":
		if !method(w, r, "POST") {
			return
		}
		h.Cleanup()
		h.monitor.Record(observability.Event{Kind: "admin", IP: h.limiter.RequestIP(r), Message: "ran cleanup"})
		ok(w, map[string]bool{"cleaned": true})
	default:
		writeError(w, 404, "not found")
	}
}
