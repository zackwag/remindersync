// Package runner syncs configured pairs one after another and reports on
// them, for `remindersync sync` and `remindersync status`.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/internal/state"
)

// ErrNoPairs means there's nothing configured to sync.
var ErrNoPairs = errors.New("no sync pairs configured; run `remindersync config` to add one")

// Opener builds the stores for a pair.
type Opener interface {
	Hub(p config.Pair) (engine.Store, error)
	Target(ctx context.Context, p config.Pair) (engine.Store, error)
}

// Options adjusts a sync.
type Options struct {
	// Pair limits the sync to the pair with this name.
	Pair   string
	DryRun bool
	Force  bool
	// Quiet leaves out pairs that had nothing to do, for the daemon's log.
	Quiet  bool
	Out    io.Writer
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// OnResult, if set, is called with each pair's outcome, except on a dry
	// run. err is nil when the pair synced and its run was recorded.
	OnResult func(p config.Pair, err error)
}

// Sync syncs each selected pair in turn. A pair that fails doesn't stop the
// others; the returned error says how many failed. Every pair's outcome is
// recorded for `status`, except on a dry run.
func Sync(ctx context.Context, cfg config.Config, db *state.DB, open Opener, opts Options) error {
	pairs, err := selectPairs(cfg, opts.Pair)
	if err != nil {
		return err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	var failed, printed int
	for _, p := range pairs {
		res, err := syncPair(ctx, p, db, open, opts, now)
		var recordErr error
		if !opts.DryRun {
			run := state.Run{FinishedAt: now(), Planned: len(res.Actions), Applied: res.Applied}
			if err != nil {
				run.Error = err.Error()
			}
			recordErr = db.RecordRun(ctx, p.ID, run)
		}
		if err != nil || recordErr != nil {
			failed++
		}
		if opts.OnResult != nil && !opts.DryRun {
			opts.OnResult(p, errors.Join(err, recordErr))
		}
		if opts.Quiet && err == nil && recordErr == nil && len(res.Actions) == 0 {
			continue
		}

		if printed > 0 {
			printf(opts.Out, "\n")
		}
		printed++
		report(opts, p, res, err)
		if recordErr != nil {
			printf(opts.Out, "  couldn't record this run: %v\n", recordErr)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d %s failed", failed, len(pairs), plural(len(pairs), "pair", "pairs"))
	}
	return nil
}

// report prints a pair's actions and how the sync went.
func report(opts Options, p config.Pair, res engine.Result, err error) {
	printf(opts.Out, "%s\n", p)
	for _, a := range res.Actions {
		printf(opts.Out, "  %s\n", a)
	}
	switch {
	case err != nil:
		printf(opts.Out, "  failed after %d of %d changes: %v\n", res.Applied, len(res.Actions), err)
		if errors.Is(err, engine.ErrTooManyChanges) {
			printf(opts.Out, "  Check the plan with -dry-run, then rerun with -force if it's what you want.\n")
		}
	case len(res.Actions) == 0:
		printf(opts.Out, "  Nothing to do.\n")
	case opts.DryRun:
		printf(opts.Out, "  %d %s planned. Dry run, so nothing was written.\n", len(res.Actions), plural(len(res.Actions), "change", "changes"))
	default:
		printf(opts.Out, "  Applied %d %s.\n", res.Applied, plural(res.Applied, "change", "changes"))
	}
}

func syncPair(ctx context.Context, p config.Pair, db *state.DB, open Opener, opts Options, now func() time.Time) (engine.Result, error) {
	hub, err := open.Hub(p)
	if err != nil {
		return engine.Result{}, fmt.Errorf("open Reminders: %w", err)
	}
	target, err := open.Target(ctx, p)
	if err != nil {
		return engine.Result{}, fmt.Errorf("open %s: %w", p.Target, err)
	}
	return engine.Sync(ctx, engine.Pair{ID: p.ID, Direction: p.Direction, Hub: hub, Target: target, Dates: p.Target.Dates(), DaysAhead: p.Target.Ahead()}, db, engine.Options{
		DryRun: opts.DryRun,
		Force:  opts.Force,
		Now:    now,
		Logger: opts.Logger,
	})
}

// Status prints what the daemon is doing, then each pair with its link
// count, unfinished creates and last run.
func Status(ctx context.Context, cfg config.Config, db *state.DB, out io.Writer) error {
	if len(cfg.Pairs) == 0 {
		return ErrNoPairs
	}
	d, err := db.Daemon(ctx)
	if err != nil {
		return err
	}
	printf(out, "Daemon: %s\n", describeDaemon(d))
	for _, p := range cfg.Pairs {
		printf(out, "\n")
		st, err := db.Status(ctx, p.ID)
		if err != nil {
			return err
		}
		printf(out, "%s (%s)\n", p, p.Direction)
		printf(out, "  Linked items:       %d\n", st.Links)
		printf(out, "  Days ahead:         %d\n", p.Target.Ahead())
		if st.Pending > 0 {
			printf(out, "  Unfinished creates: %d (the next sync recovers them)\n", st.Pending)
		}
		switch r := st.LastRun; {
		case r == nil:
			printf(out, "  Last sync:          never\n")
		case r.Error != "":
			printf(out, "  Last sync:          %s, failed after %d of %d changes: %s\n", stamp(r.FinishedAt), r.Applied, r.Planned, r.Error)
		default:
			printf(out, "  Last sync:          %s, %d %s\n", stamp(r.FinishedAt), r.Applied, plural(r.Applied, "change", "changes"))
		}
	}
	return nil
}

// processAlive reports whether a process exists, by sending it signal 0.
var processAlive = func(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func describeDaemon(d *state.Daemon) string {
	if d == nil {
		return "never started (run `remindersync install` to run it in the background)"
	}
	live := d.State == state.DaemonRunning || d.State == state.DaemonWaitingForAccess
	switch {
	case live && !processAlive(d.PID):
		return fmt.Sprintf("not running. It stopped without exiting cleanly some time after %s (pid %d), so check its log.", stamp(d.Since), d.PID)
	case d.State == state.DaemonRunning:
		return fmt.Sprintf("running since %s (pid %d)", stamp(d.Since), d.PID)
	case d.State == state.DaemonWaitingForAccess:
		return fmt.Sprintf("waiting for Reminders access since %s (pid %d). Click Allow on the macOS prompt.", stamp(d.Since), d.PID)
	case d.State == state.DaemonAccessDenied:
		return fmt.Sprintf("stopped at %s because Reminders access was denied. Allow remindersync in System Settings > Privacy & Security > Reminders, then run `remindersync install` again.", stamp(d.Since))
	default:
		return fmt.Sprintf("stopped at %s", stamp(d.Since))
	}
}

func selectPairs(cfg config.Config, name string) ([]config.Pair, error) {
	if len(cfg.Pairs) == 0 {
		return nil, ErrNoPairs
	}
	if name == "" {
		return cfg.Pairs, nil
	}
	p, ok := cfg.Find(name)
	if !ok {
		return nil, fmt.Errorf("no pair named %q", name)
	}
	return []config.Pair{p}, nil
}

func stamp(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04:05")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
