package middleware

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrustedProxyAndSpoofedWhitelist(t *testing.T) {
	l := NewLimiter(1, 1, 10, []string{"127.0.0.1"})
	r := httptest.NewRequest("GET", "http://example.com", nil)
	r.RemoteAddr = "192.0.2.1:123"
	r.Header.Set("X-Real-IP", "127.0.0.1")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	if ip := l.RequestIP(r); ip != "192.0.2.1" || l.IsWhitelisted(ip) {
		t.Fatal("spoofed whitelist")
	}
	if err := l.SetTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	r.RemoteAddr = "10.0.0.1:123"
	r.Header.Set("X-Forwarded-For", "127.0.0.1, 198.51.100.2, 10.0.0.2")
	if ip := l.RequestIP(r); ip != "198.51.100.2" {
		t.Fatal(ip)
	}
}
func TestHourlyBytesBoundary(t *testing.T) {
	b := newBytesBucket(10)
	if !b.Allow(10) || b.Allow(1) || b.Allow(-1) {
		t.Fatal("byte boundary incorrect")
	}
	b.windowEnd = time.Now().Add(-time.Second)
	if !b.Allow(10) {
		t.Fatal("quota did not reset")
	}
}
