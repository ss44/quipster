package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"quipster/internal/logbuf"
)

// LogQuery selects recent application logs. Times are RFC3339 capture times,
// not parsed message timestamps; substring matching is literal and case-sensitive.
type LogQuery struct {
	Since    string `json:"since,omitempty" jsonschema:"inclusive RFC3339 capture time lower bound"`
	Until    string `json:"until,omitempty" jsonschema:"inclusive RFC3339 capture time upper bound"`
	Contains string `json:"contains,omitempty" jsonschema:"literal case-sensitive substring of sanitized log text (max 256 bytes)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"newest matching lines (default 500 maximum 5000)"`
}

// LogResult contains sanitized application log lines in oldest-first order.
// Entries aligns one-for-one with Lines and preserves process capture timestamps.
// The escaped JSON envelope is capped at 256 KiB, including both representations.
type LogResult struct {
	Entries []logbuf.Entry `json:"entries"`
	Lines   []string       `json:"lines"`
	Count   int            `json:"count"`
}

// AppLogs serves REST and MCP from the same memory-only snapshot. It never
// queries SQLite, even when indexing has exhausted the database's busy timeout.
func (c *Collector) AppLogs(q LogQuery) (LogResult, error) {
	out := LogResult{Lines: []string{}, Entries: []logbuf.Entry{}}
	since, until, limit, err := q.bounds()
	if err != nil {
		return out, err
	}
	if c == nil || c.LogBuffer == nil {
		return out, nil
	}
	for _, e := range c.LogBuffer.Entries() {
		if (!since.IsZero() && e.Time.Before(since)) || (!until.IsZero() && e.Time.After(until)) {
			continue
		}
		line := redactLog(e.Text)
		if strings.Contains(line, q.Contains) {
			out.Entries = append(out.Entries, logbuf.Entry{Time: e.Time, Text: line})
		}
	}
	// Budget the escaped JSON of both representations, including timestamps,
	// rather than raw text alone. Reserve envelope/comma/count overhead.
	start, budget := len(out.Entries), 128
	for start > 0 && len(out.Entries)-start < limit {
		e := out.Entries[start-1]
		encoded, err := json.Marshal([]any{e, e.Text})
		if err != nil {
			return LogResult{}, fmt.Errorf("encode log entry: %w", err)
		}
		n := len(encoded) // array brackets conservatively cover both commas
		if budget+n > 256<<10 {
			break
		}
		budget += n
		start--
	}
	out.Entries = out.Entries[start:]
	for _, e := range out.Entries {
		out.Lines = append(out.Lines, e.Text)
	}
	out.Count = len(out.Lines)
	return out, nil
}

// bounds validates and normalizes shared transport query limits.
func (q LogQuery) bounds() (since, until time.Time, limit int, err error) {
	since, err = logTime(q.Since)
	if err != nil {
		return
	}
	until, err = logTime(q.Until)
	if err != nil {
		return
	}
	if len(q.Contains) > 256 {
		err = fmt.Errorf("contains must be at most 256 bytes")
		return
	}
	if !since.IsZero() && !until.IsZero() && since.After(until) {
		err = fmt.Errorf("since must not be after until")
		return
	}
	limit = q.Limit
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	return
}

// logTime validates optional transport time bounds without accessing storage.
func logTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("log time must be RFC3339")
	}
	return t, nil
}

var (
	logBearer         = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[^\s,;]+`)
	logSecret         = regexp.MustCompile(`(?i)\b([a-z0-9_-]*(?:token|api[_-]?key|password|secret)|authorization|bearer)["']?\s*[=:]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	logQuotedString   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
	logURLCredentials = regexp.MustCompile(`(https?://)[^\s/@]+@`)
	logPath           = regexp.MustCompile(`[^\s"'<>=,;]*[\\/][^\s"'<>]*`)
)

// redactLog reuses diagnostic secret scrubbing and additionally removes paths
// and labeled credentials. There is deliberately no raw-log transport option.
func redactLog(line string) string {
	line = redactSecrets(line)
	for _, entry := range strings.Split(os.Getenv("MCP_TOKENS"), ",") {
		token, _, _ := strings.Cut(strings.TrimSpace(entry), ":")
		token = strings.TrimSpace(token)
		if token != "" {
			line = strings.ReplaceAll(line, token, "[redacted]")
		}
	}
	// Consume every complete quoted string, including escapes, so a non-path's
	// closing quote cannot become a path's opening quote. Redact paths whole
	// before token matching can leak fragments; keep the shared date exemption.
	line = logQuotedString.ReplaceAllStringFunc(line, func(quoted string) string {
		inner := quoted[1 : len(quoted)-1]
		if !strings.ContainsAny(inner, `\/`) || redactLogPath(inner) == inner {
			return quoted
		}
		return "<redacted>"
	})
	line = logURLCredentials.ReplaceAllString(line, "${1}[redacted]@")
	line = logBearer.ReplaceAllString(line, "Bearer [redacted]")
	line = logSecret.ReplaceAllString(line, "$1=[redacted]")
	return logPath.ReplaceAllStringFunc(line, redactLogPath)
}

// redactLogPath exempts only a complete calendar date, never a date-shaped
// prefix or suffix of a path, so private path suffixes cannot leak.
func redactLogPath(candidate string) string {
	for _, layout := range []string{"2006/01/02", "01/02/2006"} {
		if _, err := time.Parse(layout, strings.Trim(candidate, "[]():.,;")); err == nil {
			return candidate
		}
	}
	return "<redacted>"
}
