package fonts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogAndLookup(t *testing.T) {
	cat := Catalog()
	if len(cat) == 0 {
		t.Fatal("Catalog() returned empty list")
	}
	// Mutating the returned slice must not affect the internal catalog.
	cat[0].ID = "mutated"
	if _, ok := Lookup(DefaultID); !ok {
		t.Fatalf("Lookup(%q) failed after caller mutated Catalog()", DefaultID)
	}

	for _, f := range Catalog() {
		got, ok := Lookup(f.ID)
		if !ok || got.Family != f.Family {
			t.Errorf("Lookup(%q) = %+v, %v", f.ID, got, ok)
		}
		if data, ok := File(f.ID, 400); !ok || len(data) < 1000 {
			t.Errorf("File(%q, 400) len=%d ok=%v", f.ID, len(data), ok)
		}
		if f.HasBold {
			if data, ok := File(f.ID, 700); !ok || len(data) < 1000 {
				t.Errorf("File(%q, 700) len=%d ok=%v", f.ID, len(data), ok)
			}
		} else if _, ok := File(f.ID, 700); ok {
			t.Errorf("File(%q, 700) unexpectedly succeeded for regular-only font", f.ID)
		}
	}

	if _, ok := Lookup("unknown-font"); ok {
		t.Error("expected unknown font lookup to fail")
	}
	if _, ok := File("inter", 500); ok {
		t.Error("expected unsupported weight 500 to fail")
	}
	if _, ok := File("unknown", 400); ok {
		t.Error("expected unknown font file to fail")
	}
}

func TestFileByName(t *testing.T) {
	if data, ok := FileByName("jetbrains-mono-700.ttf"); !ok || len(data) == 0 {
		t.Fatalf("FileByName(jetbrains-mono-700.ttf) failed")
	}
	if data, ok := FileByName("bangers-400.ttf"); !ok || len(data) == 0 {
		t.Fatalf("FileByName(bangers-400.ttf) failed")
	}
	bad := []string{
		"", "inter-400.woff2", "../inter-400.ttf", "inter.ttf",
		"inter-abc.ttf", "bangers-700.ttf", "missing-400.ttf", "-400.ttf",
	}
	for _, name := range bad {
		if _, ok := FileByName(name); ok {
			t.Errorf("FileByName(%q) unexpectedly succeeded", name)
		}
	}
}

func TestMaterialize(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fonts")
	gotDir, err := Materialize(dir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if gotDir != dir {
		t.Fatalf("got dir %q, want %q", gotDir, dir)
	}
	for _, f := range Catalog() {
		p := filepath.Join(dir, f.ID+"-400.ttf")
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			t.Errorf("expected materialized font %s, err=%v", p, err)
		}
	}
	// Second call is idempotent and skips unchanged files.
	if _, err := Materialize(dir); err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
}

func TestMaterializeErrorOnFileAsDir(t *testing.T) {
	blocking := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocking, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(blocking); err == nil {
		t.Fatal("expected error when target dir is a regular file")
	}
}
