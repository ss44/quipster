package diagnostics

import (
	"quipster/internal/logbuf"
	"strings"
	"testing"
	"time"
)

func TestAppLogsUsesInclusiveCaptureTimesAndNormalizesLimits(t *testing.T) {
	b := logbuf.New(0)
	c := &Collector{LogBuffer: b}
	if _, err := b.Write([]byte("1900/01/01 first\n")); err != nil {
		t.Fatal(err)
	}
	first := b.Entries()[0].Time.Format(time.RFC3339Nano)
	time.Sleep(time.Millisecond)
	if _, err := b.Write([]byte("2100/01/01 second\n")); err != nil {
		t.Fatal(err)
	}
	second := b.Entries()[1].Time.Format(time.RFC3339Nano)
	for _, tc := range []struct {
		want string
		q    LogQuery
	}{
		{"first", LogQuery{Until: first}}, {"second", LogQuery{Since: second}}, {"first", LogQuery{Since: first, Until: first}},
	} {
		out, err := c.AppLogs(tc.q)
		if err != nil || out.Count != 1 || !strings.Contains(out.Lines[0], tc.want) {
			t.Fatalf("capture-time query %+v: %+v, %v", tc.q, out, err)
		}
	}
	for _, limit := range []int{-1, 0, 5001} {
		out, err := c.AppLogs(LogQuery{Limit: limit})
		if err != nil || out.Count != 2 {
			t.Fatalf("limit %d: %+v, %v", limit, out, err)
		}
	}
}

func TestAppLogsScrubsJSONAndPrefixedCredentials(t *testing.T) {
	for _, line := range []string{`{"api_key":"unknown-value"}`, `EMBEDDINGS_API_KEY=unknown-value`, `NODE_REGISTRATION_SECRET="unknown-value"`} {
		if got := redactLog(line); strings.Contains(got, "unknown-value") {
			t.Fatalf("credential leaked: %s", got)
		}
	}
}

func TestAppLogsScrubsMCPTokensWithWhitespaceBeforeScope(t *testing.T) {
	t.Setenv("MCP_TOKENS", "example-credential :admin")
	if got := redactLog("received example-credential"); got != "received [redacted]" {
		t.Fatalf("credential was not redacted: %q", got)
	}
}

func TestAppLogsScrubsQuotedCredentials(t *testing.T) {
	for _, line := range []string{`password="two secret words"`, `api_key='two secret words'`, `Authorization: Basic dXNlcjpwYXNz`} {
		got := redactLog(line)
		for _, secret := range []string{"secret words", "dXNlcjpwYXNz"} {
			if strings.Contains(got, secret) {
				t.Fatalf("leaked credential: %s", got)
			}
		}
	}
}

func TestAppLogsValidatesBoundsAndCapsResponse(t *testing.T) {
	c := &Collector{LogBuffer: logbuf.New(0)}
	for _, q := range []LogQuery{{Contains: strings.Repeat("x", 257)}, {Since: "bad"}, {Until: "bad"}, {Since: "2026-11-12T12:00:00Z", Until: "2026-11-12T11:00:00Z"}} {
		if _, err := c.AppLogs(q); err == nil {
			t.Fatalf("accepted invalid query: %+v", q)
		}
	}
	for i := 0; i < 1000; i++ {
		if _, err := c.LogBuffer.Write([]byte(strings.Repeat("x", 1024) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.AppLogs(LogQuery{Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Join(out.Lines, "\n")) > 256<<10 {
		t.Fatal("response exceeds byte cap")
	}
}

func TestAppLogsSanitizesQuotedPathsAndUnknownCredentials(t *testing.T) {
	c := &Collector{LogBuffer: logbuf.New(0)}
	input := `file="/private folder/movie.mkv" other='C:\private folder\movie.mkv' API_TOKEN=unknown-api https://user:password@example.test/foo` + "\n"
	if _, err := c.LogBuffer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	out, err := c.AppLogs(LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"folder", "movie.mkv", "unknown-api", "user:password"} {
		if strings.Contains(strings.Join(out.Lines, "\n"), secret) {
			t.Fatalf("leaked %s: %+v", secret, out)
		}
	}
}

func TestAppLogsSanitizesAndFiltersWithoutStore(t *testing.T) {
	t.Setenv("API_TOKEN", "api-supersecret")
	t.Setenv("MCP_TOKENS", "mcp-supersecret:admin,another-secret:read")
	b := logbuf.New(0)
	c := &Collector{LogBuffer: b}
	input := "healthy\nSQLITE_BUSY file=/mnt/private/movie.mkv API_TOKEN=api-supersecret bearer=mcp-supersecret\nSQLITE_BUSY file=C:\\private\\movie.mkv Authorization: Bearer unknownsecret password=unknownpassword\n"
	if _, err := b.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	out, err := c.AppLogs(LogQuery{Contains: "SQLITE_BUSY", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || len(out.Lines) != 1 || !strings.Contains(out.Lines[0], "SQLITE_BUSY") {
		t.Fatalf("result: %+v", out)
	}
	for _, secret := range []string{"private", "unknownsecret", "unknownpassword"} {
		if strings.Contains(out.Lines[0], secret) {
			t.Fatalf("leaked %s: %v", secret, out)
		}
	}
	all, err := c.AppLogs(LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"api-supersecret", "mcp-supersecret", "private"} {
		if strings.Contains(strings.Join(all.Lines, "\n"), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	empty, err := c.AppLogs(LogQuery{Since: time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
	if err != nil || empty.Count != 0 || empty.Lines == nil {
		t.Fatalf("future query: %+v, %v", empty, err)
	}
}
