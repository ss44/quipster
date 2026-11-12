package diagnostics

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
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
type LogResult struct {
	Lines []string `json:"lines"`
	Count int      `json:"count"`
}

// AppLogs serves REST and MCP from the same memory-only snapshot. It never
// queries SQLite, even when indexing has exhausted the database's busy timeout.
func (c *Collector) AppLogs(q LogQuery) (LogResult, error) {
	out := LogResult{Lines: []string{}}
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
			out.Lines = append(out.Lines, line)
		}
	}
	start, budget := len(out.Lines), 0
	for start > 0 && len(out.Lines)-start < limit {
		n := len(out.Lines[start-1]) + 1
		if budget+n > 256<<10 {
			break
		}
		budget += n
		start--
	}
	out.Lines = out.Lines[start:]
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
	logQuotedPath     = regexp.MustCompile(`"(?:[A-Za-z]:[\\/]|/)[^"]*"|'(?:[A-Za-z]:[\\/]|/)[^']*'`)
	logURLCredentials = regexp.MustCompile(`(https?://)[^\s/@]+@`)
	logPath           = regexp.MustCompile(`(?:[A-Za-z]:[\\/]|/)[^\s"'<>]*`)
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
	line = logQuotedPath.ReplaceAllString(line, "<redacted>")
	line = logURLCredentials.ReplaceAllString(line, "${1}[redacted]@")
	line = logBearer.ReplaceAllString(line, "Bearer [redacted]")
	line = logSecret.ReplaceAllString(line, "$1=[redacted]")
	return logPath.ReplaceAllString(line, "<redacted>")
}
