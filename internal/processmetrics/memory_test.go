package processmetrics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRSSMemMBFromStatm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statm")
	if err := os.WriteFile(path, []byte("1000 512 0 0 0 0 0\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := rssMemMBFromStatm(path, 4096)
	if err != nil {
		t.Fatalf("rssMemMBFromStatm() error = %v", err)
	}
	if got != 2 {
		t.Fatalf("rssMemMBFromStatm() = %d, want 2", got)
	}
}

func TestRSSMemMBFromStatmRejectsMalformedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statm")
	if err := os.WriteFile(path, []byte("1000 nope\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := rssMemMBFromStatm(path, 4096); err == nil {
		t.Fatal("rssMemMBFromStatm() error = nil, want parse error")
	}
}

func TestCurrentMemory(t *testing.T) {
	snapshot := CurrentMemory()
	if snapshot.GoSysMemMB <= 0 {
		t.Fatalf("GoSysMemMB = %d, want positive runtime memory", snapshot.GoSysMemMB)
	}
	if snapshot.HeapAllocMemMB < 0 {
		t.Fatalf("HeapAllocMemMB = %d, want non-negative heap allocation", snapshot.HeapAllocMemMB)
	}
	if snapshot.RSSMemMB < 0 {
		t.Fatalf("RSSMemMB = %d, want non-negative RSS", snapshot.RSSMemMB)
	}
}
