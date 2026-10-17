// Package observability keeps bounded operational history on disk and metrics for this process.
package observability

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxLogBytes = 5 << 20
const maxEvents = 500

type Event struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Method     string    `json:"method,omitempty"`
	Path       string    `json:"path,omitempty"`
	IP         string    `json:"ip,omitempty"`
	Status     int       `json:"status,omitempty"`
	Bytes      int64     `json:"bytes,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Message    string    `json:"message,omitempty"`
}
type Monitor struct {
	mu                                   sync.Mutex
	path                                 string
	events                               []Event
	started                              time.Time
	requests, errors, uploads, downloads uint64
	uploadBytes, downloadBytes           int64
	logError                             string
}

func New(dir string) (*Monitor, error) {
	m := &Monitor{path: filepath.Join(dir, "events.jsonl"), started: time.Now(), events: []Event{}}
	// At most two 5 MiB files, with a bounded tail exposed to administrators.
	for _, path := range []string{m.path + ".1", m.path} {
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		s := bufio.NewScanner(io.LimitReader(f, maxLogBytes+(32<<10)))
		for s.Scan() {
			var e Event
			if json.Unmarshal(s.Bytes(), &e) == nil {
				m.append(e)
			}
		}
		err = s.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	m.Record(Event{Kind: "system", Message: "service started"})
	return m, nil
}
func (m *Monitor) append(e Event) {
	m.events = append(m.events, e)
	if len(m.events) > maxEvents {
		m.events = append([]Event(nil), m.events[len(m.events)-maxEvents:]...)
	}
}
func (m *Monitor) Record(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Method = truncate(e.Method, 32)
	e.Path = truncate(e.Path, 2048)
	e.Message = truncate(e.Message, 2048)
	e.IP = truncate(e.IP, 64)
	m.append(e)
	if e.Kind == "request" {
		m.requests++
		if e.Status >= 400 {
			m.errors++
		}
	}
	if e.Kind == "upload" {
		m.uploads++
		m.uploadBytes += e.Bytes
	}
	if e.Kind == "download" {
		m.downloads++
		m.downloadBytes += e.Bytes
	}
	if err := m.persist(e); err != nil {
		m.logError = err.Error()
		log.Printf("[monitor] %v", err)
	} else {
		m.logError = ""
	}
}
func (m *Monitor) persist(e Event) error {
	if fi, err := os.Stat(m.path); err == nil && fi.Size() >= maxLogBytes {
		if err = os.Rename(m.path, m.path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(e)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (m *Monitor) Snapshot() (map[string]any, []Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := append([]Event{}, m.events...)
	return map[string]any{"started_at": m.started, "uptime_seconds": int64(time.Since(m.started).Seconds()), "requests": m.requests, "errors": m.errors, "uploads": m.uploads, "downloads": m.downloads, "uploaded_bytes": m.uploadBytes, "downloaded_bytes": m.downloadBytes, "log_error": m.logError}, events
}

type writer struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *writer) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *writer) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}
func (m *Monitor) Wrap(next http.Handler, ip func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &writer{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		if rw.status == 0 {
			rw.status = 200
		}
		peer := ip(r)
		m.Record(Event{Kind: "request", Method: r.Method, Path: r.URL.Path, IP: peer, Status: rw.status, Bytes: rw.bytes, DurationMS: time.Since(start).Milliseconds()})
		log.Printf("%s %s %s %d %dB %s", peer, truncate(r.Method, 32), truncate(r.URL.Path, 2048), rw.status, rw.bytes, time.Since(start))
	})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
