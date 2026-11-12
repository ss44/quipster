package diagnostics

import (
	"strings"
	"testing"
	"time"

	"quipster/internal/logbuf"
)

func TestAppLogsFragmentsNeverExposeCredentialOrOversizeSuffix(t *testing.T) {
	c := &Collector{LogBuffer: logbuf.New(0)}
	for _, part := range []string{"2026/11/12 password=", "fragment-secret"} {
		writeDiagnosticFragment(t, c.LogBuffer, part)
		out, err := c.AppLogs(LogQuery{})
		if err != nil || out.Count != 0 || len(out.Entries) != 0 {
			t.Fatalf("partial line exposed: %+v %v", out, err)
		}
	}
	writeDiagnosticFragment(t, c.LogBuffer, "\n")
	first := c.LogBuffer.Entries()[0].Time
	writeDiagnosticFragment(t, c.LogBuffer, strings.Repeat("x", 100<<10)+"private-suffix")
	writeDiagnosticFragment(t, c.LogBuffer, "\nhealthy\n")
	out, err := c.AppLogs(LogQuery{Since: first.Format(time.RFC3339Nano), Until: first.Format(time.RFC3339Nano), Contains: "2026/11/12", Limit: 1})
	if err != nil || out.Count != 1 {
		t.Fatalf("capture-time filtered entry: %+v %v", out, err)
	}
	if out.Entries[0].Text != out.Lines[0] || !out.Entries[0].Time.Equal(first) || strings.Contains(out.Lines[0], "fragment-secret") {
		t.Fatalf("filtered entry unsafe/misaligned: %+v", out)
	}
	all, err := c.AppLogs(LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Count != 3 || all.Lines[1] != "[log line truncated]" || all.Lines[2] != "healthy" {
		t.Fatalf("oversized line/suffix exposed: %+v", all)
	}
}

func writeDiagnosticFragment(t *testing.T, b *logbuf.Buffer, text string) {
	t.Helper()
	if n, err := b.Write([]byte(text)); err != nil || n != len(text) {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
}
