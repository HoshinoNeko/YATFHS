package observability

import (
	"os"
	"strings"
	"testing"
)

func TestBoundedHistoryAndRotation(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Record(Event{Kind: "request", Path: strings.Repeat("a", 100000), Status: 500})
	m.Record(Event{Kind: "upload", Bytes: 42})
	m.Record(Event{Kind: "download", Bytes: 12})
	stats, events := m.Snapshot()
	if stats["errors"] != uint64(1) || stats["uploaded_bytes"] != int64(42) || stats["downloaded_bytes"] != int64(12) {
		t.Fatal(stats)
	}
	if len(events[1].Path) != 2048 {
		t.Fatal("unbounded event")
	}
	m2, err := New(dir)
	if err != nil {
		t.Fatal("persisted history unreadable:", err)
	}
	_, events = m2.Snapshot()
	if len(events) != 5 {
		t.Fatalf("lost history %d", len(events))
	}
	if err = os.Truncate(m.path, maxLogBytes); err != nil {
		t.Fatal(err)
	}
	m.Record(Event{Kind: "system", Message: "rotate"})
	if _, err = os.Stat(m.path + ".1"); err != nil {
		t.Fatal("rotation failed", err)
	}
	for i := 0; i < maxEvents+1; i++ {
		m.Record(Event{Kind: "system", Message: "bounded"})
	}
	_, events = m.Snapshot()
	if len(events) != maxEvents {
		t.Fatal("unbounded memory history")
	}
}
