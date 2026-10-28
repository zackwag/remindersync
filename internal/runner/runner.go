// Package runner syncs configured pairs one after another and reports on
// them, for `remindersync sync` and `remindersync status`.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	Out    io.Writer
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
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

	var failed int
	for i, p := range pairs {
		if i > 0 {
			printf(opts.Out, "\n")
		}
		printf(opts.Out, "%s\n", p)
		res, err := syncPair(ctx, p, db, open, opts, now)
		for _, a := range res.Actions {
			printf(opts.Out, "  %s\n", a)
		}
		switch {
		case err != nil:
			failed++
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

		if opts.DryRun {
			continue
		}
		run := state.Run{FinishedAt: now(), Planned: len(res.Actions), Applied: res.Applied}
		if err != nil {
			run.Error = err.Error()
		}
		if err := db.RecordRun(ctx, p.ID, run); err != nil {
			failed++
			printf(opts.Out, "  couldn't record this run: %v\n", err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d %s failed", failed, len(pairs), plural(len(pairs), "pair", "pairs"))
	}
	return nil
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
	return engine.Sync(ctx, engine.Pair{ID: p.ID, Direction: p.Direction, Hub: hub, Target: target}, db, engine.Options{
		DryRun: opts.DryRun,
		Force:  opts.Force,
		Now:    now,
		Logger: opts.Logger,
	})
}

// Status prints each pair with its link count, unfinished creates and last
// run.
func Status(ctx context.Context, cfg config.Config, db *state.DB, out io.Writer) error {
	if len(cfg.Pairs) == 0 {
		return ErrNoPairs
	}
	for i, p := range cfg.Pairs {
		if i > 0 {
			printf(out, "\n")
		}
		st, err := db.Status(ctx, p.ID)
		if err != nil {
			return err
		}
		printf(out, "%s (%s)\n", p, p.Direction)
		printf(out, "  Linked items:       %d\n", st.Links)
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
