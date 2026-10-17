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
	limiter := middleware.NewLimiter(cfg.RateLimit.UploadRequestsPerMin, cfg.RateLimit.DownloadRequestsPerMin, cfg.RateLimit.UploadBytesPerHour, cfg.IPWhitelist)
	if err = limiter.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("trusted proxies: %v", err)
	}
	h, err := handler.New(cfg, store, limiter)
	if err != nil {
		log.Fatalf("init handler: %v", err)
	}
	h.Cleanup()
	cleanup.Start(h.Cleanup, cfg.CleanupInterval)
	srv := &http.Server{Addr: fmt.Sprintf("%s:%s", cfg.Host, cfg.Port), Handler: withCORS(h.Routes()), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	log.Printf("[server] listening on %s (admin enabled=%t)", srv.Addr, cfg.AdminToken != "")
	if err = srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, X-Expires-At, Retry-After")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
