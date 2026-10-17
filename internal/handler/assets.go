package handler

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed uploader.js admin.js style.css admin.css
var assets embed.FS

func (h *Handler) Asset(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	name := r.URL.Path[len("/assets/"):]
	if name != "uploader.js" && name != "admin.js" && name != "style.css" && name != "admin.css" {
		http.NotFound(w, r)
		return
	}
	b, err := assets.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}
