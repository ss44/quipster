package logbuf

import (
	"strings"
	"testing"
)

func TestBufferRetainsRecentLines(t *testing.T) {
	b := New(1024)
	if _, err := b.Write([]byte("first\nsecond\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := b.Write([]byte("third\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.Len() != 3 {
		t.Fatalf("Len = %d, want 3", b.Len())
	}
	lines := b.Lines(2)
	if len(lines) != 2 || lines[0] != "second" || lines[1] != "third" {
		t.Errorf("Lines(2) = %v, want [second third]", lines)
	}
	if all := b.Lines(0); len(all) != 3 || all[0] != "first" {
		t.Errorf("Lines(0) = %v", all)
	}
}

func TestBufferHoldsPartialLine(t *testing.T) {
	b := New(1024)
	if _, err := b.Write([]byte("complete\npart")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.Len() != 1 {
		t.Fatalf("Len = %d, want 1", b.Len())
	}
	if _, err := b.Write([]byte("ial\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := b.Lines(0)
	if len(lines) != 2 || lines[1] != "partial" {
		t.Fatalf("lines = %v, want partial line completed", lines)
	}
}

func TestBufferEvictsByBytes(t *testing.T) {
	b := New(20)
	for _, line := range []string{strings.Repeat("a", 8), strings.Repeat("b", 8), strings.Repeat("c", 8)} {
		if _, err := b.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	lines := b.Lines(0)
	if len(lines) != 2 {
		t.Fatalf("len = %d, want 2 after eviction: %v", len(lines), lines)
	}
	if lines[0][0] != 'b' || lines[1][0] != 'c' {
		t.Errorf("retained = %v, want newest two lines", lines)
	}
}

func TestBufferNilSafe(t *testing.T) {
	var b *Buffer
	if _, err := b.Write([]byte("line\n")); err != nil {
		t.Fatalf("nil Write: %v", err)
	}
	if b.Len() != 0 || b.Lines(10) != nil {
		t.Error("nil buffer must report empty")
	}
}

func TestNewUsesDefaultBudget(t *testing.T) {
	b := New(0)
	if b.maxBytes != defaultMaxBytes {
		t.Errorf("maxBytes = %d, want %d", b.maxBytes, defaultMaxBytes)
	}
}
