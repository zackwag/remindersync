package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/internal/lock"
	"github.com/zackwag/remindersync/internal/reminders"
	"github.com/zackwag/remindersync/internal/runner"
	"github.com/zackwag/remindersync/internal/state"
	skylightstore "github.com/zackwag/remindersync/internal/target/skylight"
	"github.com/zackwag/remindersync/skylight"
)

// paths holds the -config and -state flags shared by sync and status.
type paths struct {
	config, state string
}

func (p *paths) register(fs *flag.FlagSet) error {
	defConfig, err := config.DefaultPath()
	if err != nil {
		return err
	}
	defState, err := state.DefaultPath()
	if err != nil {
		return err
	}
	fs.StringVar(&p.config, "config", defConfig, "config file")
	fs.StringVar(&p.state, "state", defState, "state database")
	return nil
}

func runSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: remindersync sync [flags]\n\nSync every pair once, or just one with -pair.\n\n")
		fs.PrintDefaults()
	}
	var p paths
	if err := p.register(fs); err != nil {
		return err
	}
	pair := fs.String("pair", "", "only sync the pair with this name")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing anything")
	force := fs.Bool("force", false, "skip the limit on deletes and completions")
	verbose := fs.Bool("v", false, "log debug detail, including Skylight requests")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	cfg, err := config.Load(p.config)
	if err != nil {
		return err
	}
	// A dry run writes nothing, so it can run alongside a real sync.
	if !*dryRun {
		l, err := lock.Acquire(filepath.Join(filepath.Dir(p.state), "sync.lock"))
		if err != nil {
			return err
		}
		defer func() { _ = l.Release() }()
	}
	db, err := state.Open(ctx, p.state)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	return runner.Sync(ctx, cfg, db, &opener{verbose: *verbose}, runner.Options{
		Pair:   *pair,
		DryRun: *dryRun,
		Force:  *force,
		Out:    os.Stdout,
		Logger: logger,
	})
}

func runStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: remindersync status [flags]\n\nShow what the daemon is doing, and each pair's linked items and last sync.\n\n")
		fs.PrintDefaults()
	}
	var p paths
	if err := p.register(fs); err != nil {
		return err
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	cfg, err := config.Load(p.config)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, p.state)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return runner.Status(ctx, cfg, db, os.Stdout)
}

// opener connects to Reminders and Skylight the first time a pair needs them
// and reuses the connections for later pairs.
type opener struct {
	verbose bool
	ek      *eventkit.Client
	sky     *skylight.Client
}

func (o *opener) Hub(p config.Pair) (engine.Store, error) {
	if o.ek == nil {
		c, err := eventkit.New()
		if errors.Is(err, eventkit.ErrAccessDenied) {
			return nil, fmt.Errorf("%w (grant access in System Settings > Privacy & Security > Reminders)", err)
		}
		if err != nil {
			return nil, err
		}
		o.ek = c
	}
	return reminders.NewStore(o.ek, p.Reminders.ListID), nil
}

func (o *opener) Target(_ context.Context, p config.Pair) (engine.Store, error) {
	if p.Target.Type != config.TargetSkylight {
		return nil, fmt.Errorf("unknown target type %q", p.Target.Type)
	}
	if o.sky == nil {
		c, err := newSkylightClient(o.verbose)
		if err != nil {
			return nil, err
		}
		o.sky = c
	}
	return skylightstore.NewStore(o.sky, p.Target.FrameID, p.Target.ListID), nil
}
