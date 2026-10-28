// Package state is the SQLite-backed [engine.State]: one row per linked item
// and one per create in flight, scoped by pair ID, plus each pair's last run
// and what the daemon is doing, for `remindersync status`.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/zackwag/remindersync/internal/engine"

	// Pure Go, so builds don't need a C toolchain for SQLite.
	_ "modernc.org/sqlite"
)

// migrations are applied in order, tracked by PRAGMA user_version. Append new
// ones; never edit one that has shipped.
var migrations = []string{
	`CREATE TABLE link (
		pair_id          TEXT NOT NULL,
		hub_id           TEXT NOT NULL,
		target_id        TEXT NOT NULL,
		hub_title        TEXT NOT NULL,
		hub_completed    INTEGER NOT NULL,
		target_title     TEXT NOT NULL,
		target_completed INTEGER NOT NULL,
		synced_at        TEXT NOT NULL,
		PRIMARY KEY (pair_id, hub_id),
		UNIQUE (pair_id, target_id)
	);
	CREATE TABLE pending_create (
		pair_id    TEXT NOT NULL,
		side       TEXT NOT NULL,
		source_id  TEXT NOT NULL,
		title      TEXT NOT NULL,
		started_at TEXT NOT NULL,
		PRIMARY KEY (pair_id, side, source_id)
	);`,
	`CREATE TABLE run (
		pair_id     TEXT PRIMARY KEY,
		finished_at TEXT NOT NULL,
		planned     INTEGER NOT NULL,
		applied     INTEGER NOT NULL,
		error       TEXT NOT NULL
	);`,
	`CREATE TABLE daemon (
		id    INTEGER PRIMARY KEY CHECK (id = 1),
		pid   INTEGER NOT NULL,
		state TEXT NOT NULL,
		since TEXT NOT NULL
	);`,
	// Due dates, as YYYY-MM-DD or '' for none, for targets that keep them.
	`ALTER TABLE link ADD COLUMN hub_due TEXT NOT NULL DEFAULT '';
	ALTER TABLE link ADD COLUMN target_due TEXT NOT NULL DEFAULT '';`,
}

// DefaultPath is where the state database lives unless told otherwise:
// ~/Library/Application Support/remindersync/state.db on macOS. It's
// deliberately not a cache directory, since macOS may purge those, and losing
// state turns every pair's next sync into a first sync.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remindersync", "state.db"), nil
}

// DB is an open state database. It's safe for concurrent use.
type DB struct {
	db *sql.DB
}

var _ engine.State = (*DB)(nil)

// Open opens or creates the database at path, creating its directory if
// needed, and brings the schema up to date.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("state: create directory: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("state: open %s: %w", path, err)
	}
	// One connection serializes writes inside this process, so they never
	// hit SQLITE_BUSY against each other.
	db.SetMaxOpenConns(1)

	s := &DB{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *DB) Close() error {
	return s.db.Close()
}

func (s *DB) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("state: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("state: database schema version %d is newer than this build supports (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return err
			}
			// PRAGMA doesn't take bind parameters.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("state: migrate to version %d: %w", i+1, err)
		}
	}
	return nil
}

// Links returns a pair's links, ordered by hub ID.
func (s *DB) Links(ctx context.Context, pairID string) ([]engine.Link, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT hub_id, target_id, hub_title, hub_completed, hub_due, target_title, target_completed, target_due, synced_at
		FROM link WHERE pair_id = ? ORDER BY hub_id`, pairID)
	if err != nil {
		return nil, fmt.Errorf("state: read links: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var links []engine.Link
	for rows.Next() {
		var l engine.Link
		var syncedAt string
		if err := rows.Scan(&l.HubID, &l.TargetID, &l.Hub.Title, &l.Hub.Completed, &l.Hub.Due,
			&l.Target.Title, &l.Target.Completed, &l.Target.Due, &syncedAt); err != nil {
			return nil, fmt.Errorf("state: read links: %w", err)
		}
		if l.SyncedAt, err = parseTime(syncedAt); err != nil {
			return nil, fmt.Errorf("state: link %s: %w", l.HubID, err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: read links: %w", err)
	}
	return links, nil
}

// PutLink inserts or replaces the link for l.HubID. It fails if l.TargetID is
// already linked to a different hub item.
func (s *DB) PutLink(ctx context.Context, pairID string, l engine.Link) error {
	if err := putLink(ctx, s.db, pairID, l); err != nil {
		return fmt.Errorf("state: save link %s: %w", l.HubID, err)
	}
	return nil
}

// DeleteLink removes the link for hubID, if there is one.
func (s *DB) DeleteLink(ctx context.Context, pairID, hubID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM link WHERE pair_id = ? AND hub_id = ?`, pairID, hubID)
	if err != nil {
		return fmt.Errorf("state: delete link %s: %w", hubID, err)
	}
	return nil
}

// Pending returns a pair's unfinished creates, ordered by side and source ID.
func (s *DB) Pending(ctx context.Context, pairID string) ([]engine.PendingCreate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT side, source_id, title, started_at
		FROM pending_create WHERE pair_id = ? ORDER BY side, source_id`, pairID)
	if err != nil {
		return nil, fmt.Errorf("state: read pending creates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []engine.PendingCreate
	for rows.Next() {
		var p engine.PendingCreate
		var startedAt string
		if err := rows.Scan(&p.Side, &p.SourceID, &p.Title, &startedAt); err != nil {
			return nil, fmt.Errorf("state: read pending creates: %w", err)
		}
		if p.StartedAt, err = parseTime(startedAt); err != nil {
			return nil, fmt.Errorf("state: pending create %s: %w", p.SourceID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: read pending creates: %w", err)
	}
	return out, nil
}

// BeginCreate records p. Recording the same side and source again replaces
// the earlier row.
func (s *DB) BeginCreate(ctx context.Context, pairID string, p engine.PendingCreate) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pending_create (pair_id, side, source_id, title, started_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (pair_id, side, source_id) DO UPDATE SET
			title = excluded.title,
			started_at = excluded.started_at`,
		pairID, p.Side, p.SourceID, p.Title, formatTime(p.StartedAt))
	if err != nil {
		return fmt.Errorf("state: record pending create %s: %w", p.SourceID, err)
	}
	return nil
}

// FinishCreate removes the pending create and saves l in one transaction.
func (s *DB) FinishCreate(ctx context.Context, pairID string, side engine.Side, sourceID string, l engine.Link) error {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := deletePending(ctx, tx, pairID, side, sourceID); err != nil {
			return err
		}
		return putLink(ctx, tx, pairID, l)
	})
	if err != nil {
		return fmt.Errorf("state: finish create %s: %w", sourceID, err)
	}
	return nil
}

// AbandonCreate removes the pending create without linking anything.
func (s *DB) AbandonCreate(ctx context.Context, pairID string, side engine.Side, sourceID string) error {
	if err := deletePending(ctx, s.db, pairID, side, sourceID); err != nil {
		return fmt.Errorf("state: abandon create %s: %w", sourceID, err)
	}
	return nil
}

// Run is the outcome of a pair's most recent sync that wasn't a dry run.
type Run struct {
	FinishedAt time.Time
	// Planned and Applied are how many actions the plan had and how many
	// were carried out.
	Planned int
	Applied int
	// Error is empty when the sync succeeded.
	Error string
}

// PairStatus summarizes one pair's state for `remindersync status`.
type PairStatus struct {
	Links   int
	Pending int
	// LastRun is nil if the pair has never synced.
	LastRun *Run
}

// RecordRun saves the outcome of a sync, replacing the pair's previous one.
func (s *DB) RecordRun(ctx context.Context, pairID string, r Run) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO run (pair_id, finished_at, planned, applied, error)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (pair_id) DO UPDATE SET
			finished_at = excluded.finished_at,
			planned = excluded.planned,
			applied = excluded.applied,
			error = excluded.error`,
		pairID, formatTime(r.FinishedAt), r.Planned, r.Applied, r.Error)
	if err != nil {
		return fmt.Errorf("state: record run for %s: %w", pairID, err)
	}
	return nil
}

// Status returns a pair's link and pending-create counts and its last run.
func (s *DB) Status(ctx context.Context, pairID string) (PairStatus, error) {
	var st PairStatus
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM link WHERE pair_id = ?),
			(SELECT COUNT(*) FROM pending_create WHERE pair_id = ?)`,
		pairID, pairID).Scan(&st.Links, &st.Pending)
	if err != nil {
		return PairStatus{}, fmt.Errorf("state: status for %s: %w", pairID, err)
	}

	var r Run
	var finishedAt string
	err = s.db.QueryRowContext(ctx, `SELECT finished_at, planned, applied, error FROM run WHERE pair_id = ?`, pairID).
		Scan(&finishedAt, &r.Planned, &r.Applied, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return PairStatus{}, fmt.Errorf("state: status for %s: %w", pairID, err)
	}
	if r.FinishedAt, err = parseTime(finishedAt); err != nil {
		return PairStatus{}, fmt.Errorf("state: last run for %s: %w", pairID, err)
	}
	st.LastRun = &r
	return st, nil
}

// DaemonState is what the daemon was last doing.
type DaemonState string

// Daemon states.
const (
	// DaemonWaitingForAccess means the daemon has asked for Reminders access
	// and macOS hasn't answered, which usually means a prompt is on screen.
	DaemonWaitingForAccess DaemonState = "waiting-for-access"
	DaemonRunning          DaemonState = "running"
	// DaemonAccessDenied means Reminders access was refused and the daemon
	// exited.
	DaemonAccessDenied DaemonState = "access-denied"
	DaemonStopped      DaemonState = "stopped"
)

// Daemon is the daemon's last reported state.
type Daemon struct {
	PID   int
	State DaemonState
	// Since is when it entered State.
	Since time.Time
}

// SetDaemon records the daemon's state, replacing the previous one.
func (s *DB) SetDaemon(ctx context.Context, d Daemon) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daemon (id, pid, state, since) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			pid = excluded.pid,
			state = excluded.state,
			since = excluded.since`,
		d.PID, string(d.State), formatTime(d.Since))
	if err != nil {
		return fmt.Errorf("state: record daemon state: %w", err)
	}
	return nil
}

// Daemon returns the daemon's last reported state, or nil if it has never
// run.
func (s *DB) Daemon(ctx context.Context) (*Daemon, error) {
	var d Daemon
	var state, since string
	err := s.db.QueryRowContext(ctx, `SELECT pid, state, since FROM daemon WHERE id = 1`).Scan(&d.PID, &state, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read daemon state: %w", err)
	}
	if d.Since, err = parseTime(since); err != nil {
		return nil, fmt.Errorf("state: daemon state time: %w", err)
	}
	d.State = DaemonState(state)
	return &d, nil
}

// execer is the part of *sql.DB and *sql.Tx the write helpers need.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func putLink(ctx context.Context, e execer, pairID string, l engine.Link) error {
	_, err := e.ExecContext(ctx, `
		INSERT INTO link (pair_id, hub_id, target_id, hub_title, hub_completed, hub_due, target_title, target_completed, target_due, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (pair_id, hub_id) DO UPDATE SET
			target_id = excluded.target_id,
			hub_title = excluded.hub_title,
			hub_completed = excluded.hub_completed,
			hub_due = excluded.hub_due,
			target_title = excluded.target_title,
			target_completed = excluded.target_completed,
			target_due = excluded.target_due,
			synced_at = excluded.synced_at`,
		pairID, l.HubID, l.TargetID, l.Hub.Title, l.Hub.Completed, l.Hub.Due,
		l.Target.Title, l.Target.Completed, l.Target.Due, formatTime(l.SyncedAt))
	return err
}

func deletePending(ctx context.Context, e execer, pairID string, side engine.Side, sourceID string) error {
	_, err := e.ExecContext(ctx, `DELETE FROM pending_create WHERE pair_id = ? AND side = ? AND source_id = ?`,
		pairID, side, sourceID)
	return err
}

func (s *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// Times are stored as RFC 3339 text in UTC, which sorts and reads well.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
