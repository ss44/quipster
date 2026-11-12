package diagnostics

import (
	"testing"

	"quipster/internal/logbuf"
)

func TestLogQuotedStringPairing(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{"absolute POSIX", `msg="hello" /public/a file="/private folder/movie with spaces.mkv" failed`, `msg="hello" <redacted> file=<redacted> failed`},
		{"relative POSIX", `msg="hello" /public/a file="private folder/movie with spaces.mkv" failed`, `msg="hello" <redacted> file=<redacted> failed`},
		{"Windows drive", `msg="hello" C:\public\a file="C:\private folder\movie with spaces.mkv" failed`, `msg="hello" <redacted> file=<redacted> failed`},
		{"relative Windows", `msg='hello' public\a file='private folder\movie with spaces.mkv' failed`, `msg='hello' <redacted> file=<redacted> failed`},
		{"escaped double quote", `msg="hello" /public/a file="/private folder/movie \"title\" with spaces.mkv" failed`, `msg="hello" <redacted> file=<redacted> failed`},
		{"escaped single quote", `msg='hello' /public/a file='private folder/movie \'title\' with spaces.mkv' failed`, `msg='hello' <redacted> file=<redacted> failed`},
		{"escaped trailing backslash", `msg="hello" /public/a file="C:\\private folder\\" next="done"`, `msg="hello" <redacted> file=<redacted> next="done"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactLog(tc.line); got != tc.want {
				t.Errorf("redactLog() = %q, want %q", got, tc.want)
			}
			c := &Collector{LogBuffer: logbuf.New(0)}
			if _, err := c.LogBuffer.Write([]byte(tc.line + "\n")); err != nil {
				t.Fatal(err)
			}
			out, err := c.AppLogs(LogQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if out.Count != 1 || len(out.Lines) != 1 || len(out.Entries) != 1 {
				t.Fatalf("unexpected log result: %+v", out)
			}
			if out.Lines[0] != tc.want || out.Entries[0].Text != tc.want {
				t.Errorf("AppLogs() = %+v, want text %q", out, tc.want)
			}
		})
	}
}
