package middleware

import (
	"net"
	"net/http"
	"strings"
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
	bytes     int64
	maxBytes  int64
	windowEnd time.Time
	mu        sync.Mutex
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
	if n < 0 || n > b.maxBytes-b.bytes {
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
	trusted     []*net.IPNet

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
		// Retain live hourly quotas; resetting every ten minutes allowed quota bypass.
		now := time.Now()
		for ip, b := range l.uploads {
			b.mu.Lock()
			stale := now.Sub(b.lastRefill) > time.Hour
			b.mu.Unlock()
			if stale {
				delete(l.uploads, ip)
			}
		}
		for ip, b := range l.downloads {
			b.mu.Lock()
			stale := now.Sub(b.lastRefill) > time.Hour
			b.mu.Unlock()
			if stale {
				delete(l.downloads, ip)
			}
		}
		for ip, b := range l.uploadBytes {
			b.mu.Lock()
			stale := now.After(b.windowEnd)
			b.mu.Unlock()
			if stale {
				delete(l.uploadBytes, ip)
			}
		}
		l.mu.Unlock()
	}
}

// ExtractIP uses the socket peer. Proxy headers are accepted only from configured proxies.
func ExtractIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (l *Limiter) SetTrustedProxies(cidrs []string) error {
	for _, value := range cidrs {
		if ip := net.ParseIP(value); ip != nil {
			if ip.To4() != nil {
				value += "/32"
			} else {
				value += "/128"
			}
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return err
		}
		l.trusted = append(l.trusted, network)
	}
	return nil
}
func (l *Limiter) IsTrustedProxy(ip string) bool {
	for _, n := range l.trusted {
		if n.Contains(net.ParseIP(ip)) {
			return true
		}
	}
	return false
}
func (l *Limiter) RequestIP(r *http.Request) string {
	peer := ExtractIP(r)
	if !l.IsTrustedProxy(peer) {
		return peer
	}
	// Walk from the proxy nearest us to the first untrusted hop.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			continue
		}
		peer = ip.String()
		if !l.IsTrustedProxy(peer) {
			return peer
		}
	}
	if peer == ExtractIP(r) {
		if ip := net.ParseIP(r.Header.Get("X-Real-IP")); ip != nil {
			return ip.String()
		}
	}
	return peer
}
