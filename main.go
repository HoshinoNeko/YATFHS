package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
	"github.com/HoshinoNeko/YATFHS/internal/cleanup"
	"github.com/HoshinoNeko/YATFHS/internal/config"
	"github.com/HoshinoNeko/YATFHS/internal/handler"
	"github.com/HoshinoNeko/YATFHS/internal/middleware"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	store, err := storage.New(cfg.StorageDir)
	if err != nil {
		log.Fatalf("init storage: %v", err)
	}
	log.Printf("[storage] initialized at %s", cfg.StorageDir)

	limiter := middleware.NewLimiter(
		cfg.RateLimit.UploadRequestsPerMin,
		cfg.RateLimit.DownloadRequestsPerMin,
		cfg.RateLimit.UploadBytesPerHour,
		cfg.IPWhitelist,
	)
	log.Printf("[ratelimit] upload=%d/min download=%d/min upload_quota=%dMB/hr whitelist=%v",
		cfg.RateLimit.UploadRequestsPerMin,
		cfg.RateLimit.DownloadRequestsPerMin,
		cfg.RateLimit.UploadBytesPerHour/1024/1024,
		cfg.IPWhitelist,
	)

	cleanup.Start(store, cfg.CleanupInterval)
	log.Printf("[cleanup] interval=%s", cfg.CleanupInterval)

	h := handler.New(cfg, store, limiter)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			h.Index(w, r)
			return
		}
		// /{id}/{filename} pattern
		h.Download(w, r)
	})
	mux.HandleFunc("/api/v1/upload", h.Upload)
	mux.HandleFunc("/api/v1/stats", h.Stats)

	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%s", cfg.Host, cfg.Port),
		Handler:      withLogging(withCORS(mux)),
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	log.Printf("[server] listening on %s (base_url=%s)", srv.Addr, cfg.BaseURL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %s %d %v", middleware.ExtractIP(r), r.Method, r.URL.Path, rw.code, time.Since(start))
	})
}

type responseWriter struct {
	http.ResponseWriter
	code int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.code = code
	rw.ResponseWriter.WriteHeader(code)
}
