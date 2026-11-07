package diagnostics

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"quipster/internal/store"
)

type sqlitePragmas struct {
	JournalMode      string `json:"journalMode,omitempty"`
	Reason           string `json:"reason,omitempty"`
	BusyTimeoutMS    int64  `json:"busyTimeoutMs"`
	ForeignKeys      int64  `json:"foreignKeys"`
	Synchronous      int64  `json:"synchronous"`
	JournalSizeLimit int64  `json:"journalSizeLimitBytes"`
	Available        bool   `json:"available"`
}

type sqliteSnapshot struct {
	Writer    sqlitePragmas `json:"writer"`
	SampledAt int64         `json:"sampledAt"`
}

type connectionPool struct {
	Reason             string `json:"reason,omitempty"`
	WaitCount          int64  `json:"waitCount"`
	WaitDurationNS     int64  `json:"waitDurationNs"`
	MaxOpenConnections int    `json:"maxOpenConnections"`
	OpenConnections    int    `json:"openConnections"`
	InUse              int    `json:"inUse"`
	Idle               int    `json:"idle"`
	Available          bool   `json:"available"`
}

type sqlitePools struct {
	Writer connectionPool `json:"writer"`
}

func writerDB(st *store.Store) *sql.DB {
	if st == nil {
		return nil
	}
	return st.DB()
}

func writerPoolStats(st *store.Store) sqlitePools {
	db := writerDB(st)
	if db == nil {
		return sqlitePools{Writer: connectionPool{Reason: "store_unavailable"}}
	}
	s := db.Stats()
	return sqlitePools{Writer: connectionPool{
		Available: true, MaxOpenConnections: s.MaxOpenConnections,
		OpenConnections: s.OpenConnections, InUse: s.InUse, Idle: s.Idle,
		WaitCount: s.WaitCount, WaitDurationNS: s.WaitDuration.Nanoseconds(),
	}}
}

func sampleSQLite(ctx context.Context, st *store.Store) sqliteSnapshot {
	out := sqliteSnapshot{SampledAt: time.Now().UnixMilli()}
	db := writerDB(st)
	if db == nil {
		out.Writer.Reason = "store_unavailable"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	out.Writer = samplePragmas(ctx, db)
	return out
}

func samplePragmas(ctx context.Context, db *sql.DB) (sample sqlitePragmas) {
	conn, err := db.Conn(ctx)
	if err != nil {
		sample.Reason = sqliteSampleReason(ctx, err)
		return sample
	}
	defer func() {
		if err := conn.Close(); err != nil {
			sample = sqlitePragmas{Reason: "unavailable"}
		}
	}()
	queries := []struct {
		dest  any
		query string
	}{
		{&sample.JournalMode, "PRAGMA journal_mode"},
		{&sample.BusyTimeoutMS, "PRAGMA busy_timeout"},
		{&sample.ForeignKeys, "PRAGMA foreign_keys"},
		{&sample.Synchronous, "PRAGMA synchronous"},
		{&sample.JournalSizeLimit, "PRAGMA journal_size_limit"},
	}
	for _, q := range queries {
		if err := conn.QueryRowContext(ctx, q.query).Scan(q.dest); err != nil {
			return sqlitePragmas{Reason: sqliteSampleReason(ctx, err)}
		}
	}
	sample.JournalMode = strings.ToLower(sample.JournalMode)
	switch sample.JournalMode {
	case "delete", "truncate", "persist", "memory", "wal", "off":
	default:
		return sqlitePragmas{Reason: "unavailable"}
	}
	sample.Available = true
	return sample
}

func sqliteSampleReason(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	return "unavailable"
}
