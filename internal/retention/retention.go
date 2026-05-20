// Package retention prunes the append-only operational tables
// (jetmon_check_history, jetmon_audit_log) down to a configured age. It is the
// storage backstop behind the CHECK_HISTORY_MODE / AUDIT_LOG_MODE write knobs:
// the modes bound how fast the tables grow, retention bounds how large they
// get over time.
//
// Cleanup deletes in PK-ordered chunks with a pause between chunks so it never
// monopolizes the connection pool, and coordinates across a multi-host fleet
// with a MySQL advisory lock so only one host prunes a given table at a time.
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	// DefaultChunkSize is the number of rows deleted per DELETE statement.
	// Small enough to keep each delete a short, low-lock-footprint operation.
	DefaultChunkSize = 5000
	// DefaultChunkPause is the gap between chunk deletes, yielding to the
	// check pipeline during a large prune.
	DefaultChunkPause = 50 * time.Millisecond
	// lockAcquireTimeoutSec bounds how long a host waits for the advisory lock
	// before concluding another host is already pruning and skipping.
	lockAcquireTimeoutSec = 5
)

// Options controls a retention run. A per-table day count of 0 disables
// pruning for that table.
type Options struct {
	CheckHistoryDays int
	AuditLogDays     int
	DryRun           bool
	ChunkSize        int
	ChunkPause       time.Duration
}

// TableResult reports the outcome of pruning one table.
type TableResult struct {
	Table         string
	RetentionDays int
	Cutoff        time.Time
	DeletedRows   int64 // dry-run: rows that *would* be deleted
	DryRun        bool
	Skipped       bool
	SkipReason    string
	Duration      time.Duration
}

// Result aggregates per-table outcomes for one run.
type Result struct {
	Tables []TableResult
}

type tableSpec struct {
	name    string
	timeCol string
	lockKey string
	days    func(Options) int
}

var specs = []tableSpec{
	{
		name:    "jetmon_check_history",
		timeCol: "checked_at",
		lockKey: "jetmon_retention_check_history",
		days:    func(o Options) int { return o.CheckHistoryDays },
	},
	{
		name:    "jetmon_audit_log",
		timeCol: "created_at",
		lockKey: "jetmon_retention_audit_log",
		days:    func(o Options) int { return o.AuditLogDays },
	},
}

// Run prunes every configured table. It returns a partial Result alongside the
// first error so callers can report what was done before the failure.
func Run(ctx context.Context, db *sql.DB, opts Options) (Result, error) {
	if db == nil {
		return Result{}, fmt.Errorf("retention: nil db")
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = DefaultChunkSize
	}
	if opts.ChunkPause <= 0 {
		opts.ChunkPause = DefaultChunkPause
	}
	var result Result
	for _, spec := range specs {
		tr, err := runTable(ctx, db, spec, opts)
		result.Tables = append(result.Tables, tr)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func runTable(ctx context.Context, db *sql.DB, spec tableSpec, opts Options) (TableResult, error) {
	days := spec.days(opts)
	tr := TableResult{Table: spec.name, RetentionDays: days, DryRun: opts.DryRun}
	if days <= 0 {
		tr.Skipped = true
		tr.SkipReason = "retention disabled (days <= 0)"
		return tr, nil
	}
	start := time.Now()
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	tr.Cutoff = cutoff

	// Pin a single connection so GET_LOCK / DELETE / RELEASE_LOCK all run on
	// it — MySQL advisory locks are connection-scoped.
	conn, err := db.Conn(ctx)
	if err != nil {
		return tr, fmt.Errorf("retention: acquire conn for %s: %w", spec.name, err)
	}
	defer conn.Close()

	locked, err := acquireLock(ctx, conn, spec.lockKey)
	if err != nil {
		return tr, fmt.Errorf("retention: lock %s: %w", spec.name, err)
	}
	if !locked {
		tr.Skipped = true
		tr.SkipReason = "another host holds the retention lock"
		return tr, nil
	}
	defer releaseLock(conn, spec.lockKey)

	if opts.DryRun {
		var count int64
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s < ?", spec.name, spec.timeCol)
		if err := conn.QueryRowContext(ctx, q, cutoff).Scan(&count); err != nil {
			return tr, fmt.Errorf("retention: dry-run count %s: %w", spec.name, err)
		}
		tr.DeletedRows = count
		tr.Duration = time.Since(start)
		return tr, nil
	}

	// Oldest rows sit at the low end of the PK, so PK-ascending DELETEs with
	// the time predicate stay near the start of the table and never need a
	// secondary index on the time column.
	delQ := fmt.Sprintf("DELETE FROM %s WHERE %s < ? ORDER BY id ASC LIMIT ?", spec.name, spec.timeCol)
	for {
		res, err := conn.ExecContext(ctx, delQ, cutoff, opts.ChunkSize)
		if err != nil {
			tr.Duration = time.Since(start)
			return tr, fmt.Errorf("retention: delete %s: %w", spec.name, err)
		}
		n, _ := res.RowsAffected()
		tr.DeletedRows += n
		if n < int64(opts.ChunkSize) {
			break
		}
		select {
		case <-ctx.Done():
			tr.Duration = time.Since(start)
			return tr, ctx.Err()
		case <-time.After(opts.ChunkPause):
		}
	}
	tr.Duration = time.Since(start)
	return tr, nil
}

func acquireLock(ctx context.Context, conn *sql.Conn, key string) (bool, error) {
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", key, lockAcquireTimeoutSec).Scan(&got); err != nil {
		return false, err
	}
	// GET_LOCK returns 1 on success, 0 on timeout, NULL on error.
	return got.Valid && got.Int64 == 1, nil
}

func releaseLock(conn *sql.Conn, key string) {
	var discarded sql.NullInt64
	// Use a fresh background context so release still fires if the run ctx
	// was cancelled mid-prune.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", key).Scan(&discarded)
}
