package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// tokenBucket is a simple per-IP token bucket rate limiter.
type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func newBucket(maxTokens float64, refillRate float64) *tokenBucket {
	return &tokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (b *tokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// bytesBucket tracks upload bytes per hour per IP.
type bytesBucket struct {
	bytes      int64
	maxBytes   int64
	windowEnd  time.Time
	mu         sync.Mutex
}

func newBytesBucket(maxBytes int64) *bytesBucket {
	return &bytesBucket{
		maxBytes:  maxBytes,
		windowEnd: time.Now().Add(time.Hour),
	}
}

func (b *bytesBucket) Allow(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	if now.After(b.windowEnd) {
		b.bytes = 0
		b.windowEnd = now.Add(time.Hour)
	}
	if b.bytes+n > b.maxBytes {
		return false
	}
	b.bytes += n
	return true
}

// Limiter holds per-IP limiters with automatic cleanup.
type Limiter struct {
	mu          sync.RWMutex
	uploads     map[string]*tokenBucket
	downloads   map[string]*tokenBucket
	uploadBytes map[string]*bytesBucket
	whitelist   map[string]bool

	uploadRPM   int
	downloadRPM int
	uploadBPH   int64
}

func NewLimiter(uploadRPM, downloadRPM int, uploadBPH int64, whitelist []string) *Limiter {
	l := &Limiter{
		uploads:     make(map[string]*tokenBucket),
		downloads:   make(map[string]*tokenBucket),
		uploadBytes: make(map[string]*bytesBucket),
		whitelist:   make(map[string]bool),
		uploadRPM:   uploadRPM,
		downloadRPM: downloadRPM,
		uploadBPH:   uploadBPH,
	}
	for _, ip := range whitelist {
		l.whitelist[ip] = true
	}
	// Cleanup stale buckets every 10 minutes
	go l.cleanupLoop()
	return l
}

func (l *Limiter) IsWhitelisted(ip string) bool {
	return l.whitelist[ip]
}

func (l *Limiter) AllowUpload(ip string) bool {
	if l.IsWhitelisted(ip) {
		return true
	}
	l.mu.Lock()
	b, ok := l.uploads[ip]
	if !ok {
		b = newBucket(float64(l.uploadRPM), float64(l.uploadRPM)/60.0)
		l.uploads[ip] = b
	}
	l.mu.Unlock()
	return b.Allow()
}

func (l *Limiter) AllowDownload(ip string) bool {
	if l.IsWhitelisted(ip) {
		return true
	}
	l.mu.Lock()
	b, ok := l.downloads[ip]
	if !ok {
		b = newBucket(float64(l.downloadRPM), float64(l.downloadRPM)/60.0)
		l.downloads[ip] = b
	}
	l.mu.Unlock()
	return b.Allow()
}

func (l *Limiter) AllowUploadBytes(ip string, size int64) bool {
	if l.IsWhitelisted(ip) {
		return true
	}
	l.mu.Lock()
	b, ok := l.uploadBytes[ip]
	if !ok {
		b = newBytesBucket(l.uploadBPH)
		l.uploadBytes[ip] = b
	}
	l.mu.Unlock()
	return b.Allow(size)
}

func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		l.mu.Lock()
		// Simple cleanup: reset all maps periodically
		// In production you'd track last-access time instead
		l.uploads = make(map[string]*tokenBucket)
		l.downloads = make(map[string]*tokenBucket)
		l.uploadBytes = make(map[string]*bytesBucket)
		l.mu.Unlock()
	}
}

// ExtractIP gets the real client IP, respecting X-Forwarded-For and X-Real-IP.
func ExtractIP(r *http.Request) string {
	// X-Real-IP (set by nginx etc.)
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		if parsed := net.ParseIP(ip); parsed != nil {
			return parsed.String()
		}
	}
	// X-Forwarded-For (leftmost = original client)
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := splitAndTrim(fwd, ',')
		if len(parts) > 0 {
			if parsed := net.ParseIP(parts[0]); parsed != nil {
				return parsed.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func splitAndTrim(s string, sep rune) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == sep {
			out = append(out, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimSpace(s[start:]))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}
