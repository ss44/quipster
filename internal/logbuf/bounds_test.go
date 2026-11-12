package logbuf

import (
	"strings"
	"testing"
)

func TestOversizedLogicalLineCannotExposeUnredactableTail(t *testing.T) {
	b := New(128)
	for _, p := range []string{strings.Repeat("x", 1000), "sensitive-tail\nnext\n"} {
		if _, err := b.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	got := b.Lines(0)
	if len(got) != 2 || got[0] != "[log line truncated]" || got[1] != "next" {
		t.Fatalf("oversized logical line must be replaced, not expose a credential/path suffix: %v", got)
	}
}

func TestPartialBackingAllocationSharesByteBudget(t *testing.T) {
	b := New(40)
	for _, p := range []string{"first-line\n", strings.Repeat("p", 17), strings.Repeat("q", 6), strings.Repeat("r", 30)} {
		if _, err := b.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
		if b.bytes+cap(b.partial) > b.maxBytes {
			t.Fatalf("backing allocation %d + %d exceeds %d", b.bytes, cap(b.partial), b.maxBytes)
		}
	}
}

func TestBufferHardLineBounds(t *testing.T) {
	b := New(0)
	if _, err := b.Write([]byte(strings.Repeat("\n", 20000))); err != nil {
		t.Fatal(err)
	}
	if b.Len() > 10000 {
		t.Fatalf("retained %d lines, want at most 10000", b.Len())
	}
	if _, err := b.Write([]byte(strings.Repeat("x", 1<<20) + "\n")); err != nil {
		t.Fatal(err)
	}
	got := b.Lines(1)
	if len(got) != 1 || len(got[0]) > 16<<10 {
		t.Fatal("per-line cap exceeded")
	}
}

func TestHugeWritesRespectCombinedBudget(t *testing.T) {
	b := New(32)
	for _, input := range []string{strings.Repeat("x", 1<<20) + "\n", "small\n" + strings.Repeat("p", 1<<20), "tail\n"} {
		n, err := b.Write([]byte(input))
		if err != nil || n != len(input) {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if b.bytes+len(b.partial) > b.maxBytes {
			t.Fatalf("retained %d complete + %d partial exceeds %d", b.bytes, len(b.partial), b.maxBytes)
		}
		for _, line := range b.Lines(0) {
			if len(line)+1 > b.maxBytes {
				t.Fatal("oversized complete line")
			}
		}
	}
}
