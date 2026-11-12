package diagnostics

import (
	"encoding/json"
	"quipster/internal/logbuf"
	"strings"
	"testing"
	"time"
)

func TestAppLogsReturnsAlignedCaptureEntriesWithinJSONBudget(t *testing.T) {
	b := logbuf.New(0)
	c := &Collector{LogBuffer: b}
	if _, err := b.Write([]byte("incomplete")); err != nil {
		t.Fatal(err)
	}
	empty, err := c.AppLogs(LogQuery{})
	if err != nil || empty.Count != 0 {
		t.Fatalf("fragment exposed: %+v %v", empty, err)
	}
	for i := 0; i < 1000; i++ {
		if _, err = b.Write([]byte(strings.Repeat("<", 1024) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.AppLogs(LogQuery{Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	assertLogCaptureEntries(t, data, b.Entries())
	if len(data) > 256<<10 {
		t.Fatalf("JSON payload exceeds budget: %d", len(data))
	}
}

func assertLogCaptureEntries(t *testing.T, data []byte, captured []logbuf.Entry) {
	t.Helper()
	var wire struct {
		Lines   []string       `json:"lines"`
		Entries []logbuf.Entry `json:"entries"`
		Count   int            `json:"count"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Count == 0 || len(wire.Entries) != wire.Count || len(wire.Lines) != wire.Count {
		t.Fatalf("missing/misaligned entries: lines=%d entries=%d count=%d", len(wire.Lines), len(wire.Entries), wire.Count)
	}
	offset := len(captured) - wire.Count
	for i, e := range wire.Entries {
		if !e.Time.Equal(captured[offset+i].Time) || e.Text != wire.Lines[i] || e.Time.Before(time.Now().Add(-time.Minute)) {
			t.Fatalf("entry %d lost capture time/text: %+v", i, e)
		}
	}
}

func TestLogRedactionPreservesDatesButNotPathSuffixes(t *testing.T) {
	for _, date := range []string{"2026/11/12", "2026/11/12 12:34:56", "2026-11-12T12:34:56Z", "11/12/2026", "[2026/11/12]", "(2026/11/12)", "2026/11/12:", "2026/11/12,"} {
		if got := redactLog("at " + date + " done"); got != "at "+date+" done" {
			t.Errorf("date corrupted: %q", got)
		}
	}
	for _, path := range []string{`/private/movie.mkv`, `C:\private\movie.mkv`, `C:/private/movie.mkv`, `\\server\private\movie.mkv`, `/2026/11/12/movie.mkv`, `2026/11/12/private/movie.mkv`, `prefix/private/movie.mkv`, `"/private folder/movie.mkv"`, `'C:\private folder\movie.mkv'`, `"\\server\private folder\movie.mkv"`, `"\private folder\movie.mkv"`, `"\\server\private\movie.mkv with spaces"`, `"\private\movie.mkv with spaces"`, `2026/11/12.movie.mkv`, `2026/11/12(private)/movie.mkv`, `/private/movie=hidden.mkv`, `/private/movie,hidden.mkv`, `/private/movie;hidden.mkv`} {
		if got := redactLog("file=" + path + " failed"); strings.Contains(got, "private") || strings.Contains(got, "movie.mkv") || strings.Contains(got, "with spaces") || strings.Contains(got, "hidden.mkv") {
			t.Errorf("path leaked: %q", got)
		}
	}
}
