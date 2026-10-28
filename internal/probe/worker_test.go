package probe

import (
	"testing"
)

func TestClampMin(t *testing.T) {
	if got := clampMin(5.0); got != 5.0 {
		t.Errorf("clampMin(5.0) = %v, want 5.0", got)
	}
	if got := clampMin(-1.2); got != 0 {
		t.Errorf("clampMin(-1.2) = %v, want 0", got)
	}
}

func TestDetectSidecarLang(t *testing.T) {
	if got := detectSidecarLang("some_file.srt"); got != "en" {
		t.Errorf("detectSidecarLang() = %v, want en", got)
	}
}

func TestPipelineSem(t *testing.T) {
	p := &Pipeline{ExtractConns: 3}
	s := p.sem()
	if cap(s) != 3 {
		t.Errorf("expected cap 3, got %d", cap(s))
	}
	
	// Default
	p2 := &Pipeline{}
	s2 := p2.sem()
	if cap(s2) != 2 {
		t.Errorf("expected default cap 2, got %d", cap(s2))
	}
}
