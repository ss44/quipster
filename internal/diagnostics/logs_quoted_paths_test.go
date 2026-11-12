package diagnostics

import (
	"testing"

	"quipster/internal/logbuf"
)

func TestQuotedPathsPreserveCompleteDatesAndSecretPriority(t *testing.T) {
	for _, date := range []string{`"2026/11/12"`, `'11/12/2026'`, `"[2026/11/12]"`, `'2026/11/12:'`} {
		line := "at " + date + " done"
		if got := redactLog(line); got != line {
			t.Errorf("complete date corrupted: got %q, want %q", got, line)
		}
	}
	for _, path := range []string{`"2026/11/12/private folder/movie with spaces.mkv"`, `'11/12/2026/private folder/movie with spaces.mkv'`} {
		if got := redactLog("file=" + path); got != "file=<redacted>" {
			t.Errorf("date-prefixed path leaked: %q", got)
		}
	}
	// Configured secrets must be scrubbed before a date exemption can apply.
	t.Setenv("API_TOKEN", "2026/11/12")
	t.Setenv("MCP_TOKENS", " 11/12/2026 :diagnostics")
	if got := redactLog(`api="2026/11/12" mcp='11/12/2026'`); got != `api="[redacted]" mcp='[redacted]'` {
		t.Errorf("configured secrets lost priority: %q", got)
	}
}

func TestQuotedRelativePathsAreFullyRedacted(t *testing.T) {
	for _, path := range []string{
		`"private folder/movie with spaces.mkv"`,
		`'private folder/movie with spaces.mkv'`,
		`"private folder\movie with spaces.mkv"`,
		`'private folder\movie with spaces.mkv'`,
		`"./private folder/movie with spaces.mkv"`,
		`'..\private folder\movie with spaces.mkv'`,
	} {
		t.Run(path, func(t *testing.T) {
			line := "file=" + path + " failed"
			const want = "file=<redacted> failed"
			if got := redactLog(line); got != want {
				t.Errorf("redactLog() = %q, want %q", got, want)
			}

			c := &Collector{LogBuffer: logbuf.New(0)}
			if _, err := c.LogBuffer.Write([]byte(line + "\n")); err != nil {
				t.Fatal(err)
			}
			out, err := c.AppLogs(LogQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if out.Count != 1 || len(out.Lines) != 1 || len(out.Entries) != 1 {
				t.Fatalf("unexpected log result: %+v", out)
			}
			if out.Lines[0] != want || out.Entries[0].Text != want {
				t.Errorf("AppLogs() exposed a path fragment: %+v", out)
			}
		})
	}
}
