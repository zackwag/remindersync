package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/alert"
	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/daemon"
	"github.com/zackwag/remindersync/internal/lock"
	"github.com/zackwag/remindersync/internal/notify"
	"github.com/zackwag/remindersync/internal/runner"
	"github.com/zackwag/remindersync/internal/state"
)

const (
	defaultPoll = time.Minute
	minPoll     = 10 * time.Second
	// alertAfter is how long a pair fails before you're told, so a short
	// outage on the target's side passes quietly.
	alertAfter = 10 * time.Minute
	// maxLog is how big daemon.log gets before it's rotated to daemon.log.1.
	maxLog = 5 << 20
)

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: remindersync daemon [flags]\n\nSync every pair whenever Reminders changes and every -poll for target changes,\nuntil stopped. `remindersync install` runs it with launchd.\n\n")
		fs.PrintDefaults()
	}
	var p paths
	if err := p.register(fs); err != nil {
		return err
	}
	poll := fs.Duration("poll", defaultPoll, "how often to check targets for changes")
	verbose := fs.Bool("v", false, "log debug detail, including Skylight requests")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *poll < minPoll {
		return fmt.Errorf("-poll must be at least %s", minPoll)
	}

	dir := filepath.Dir(p.state)
	l, err := lock.Acquire(filepath.Join(dir, "daemon.lock"))
	if errors.Is(err, lock.ErrHeld) {
		return errors.New("another remindersync daemon is already running")
	}
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()

	db, err := state.Open(ctx, p.state)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	setState := func(s state.DaemonState) {
		d := state.Daemon{PID: os.Getpid(), State: s, Since: time.Now()}
		if err := db.SetDaemon(context.WithoutCancel(ctx), d); err != nil {
			logger.Error("couldn't record the daemon's state", "err", err)
		}
	}

	logger.Info("starting", "pid", os.Getpid(), "poll", *poll)
	// New blocks while the permission prompt is up, which after an upgrade
	// can be a while. Recording that first lets `status` and `install` say
	// what it's waiting for.
	setState(state.DaemonWaitingForAccess)
	ek, err := eventkit.New()
	if errors.Is(err, eventkit.ErrAccessDenied) {
		setState(state.DaemonAccessDenied)
		logger.Error("Reminders access denied. Allow remindersync in System Settings > Privacy & Security > Reminders, then run `remindersync install` again.")
		if err := notify.Send("Reminders access denied", "Allow ReminderSync in System Settings > Privacy & Security > Reminders, then run remindersync install again."); err != nil {
			logger.Error("couldn't show an alert", "err", err)
		}
		// A clean exit, so launchd doesn't restart it into the same refusal.
		return nil
	}
	if err != nil {
		return err
	}
	changes, err := ek.WatchChanges(ctx)
	if err != nil {
		return err
	}
	setState(state.DaemonRunning)
	defer setState(state.DaemonStopped)

	op := &opener{verbose: *verbose, ek: ek}
	ip, err := defaultInstallPaths()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	replaced, err := daemon.Replaced(exe)
	if err != nil {
		return err
	}
	alerts := &alert.Tracker{After: alertAfter, Now: time.Now, Notify: notify.Send, Logger: logger}
	out := daemon.LogWriter(os.Stdout, time.Now)
	err = daemon.Run(ctx, daemon.Options{
		Changes: changes,
		Sync: func(ctx context.Context) error {
			if rotated, err := daemon.RotateLog(ip.log, maxLog, os.Stdout, os.Stderr); err != nil {
				logger.Error("couldn't rotate the log", "err", err)
			} else if rotated {
				logger.Info("rotated the log", "old", ip.log+".1")
			}
			// Reloaded every time, so pairs added or removed with
			// `remindersync config` apply without a restart.
			cfg, err := config.Load(p.config)
			alerts.Report("config.toml", err)
			if err != nil {
				return err
			}
			l, err := lock.Acquire(filepath.Join(dir, "sync.lock"))
			if err != nil {
				return err
			}
			defer func() { _ = l.Release() }()
			return runner.Sync(ctx, cfg, db, op, runner.Options{
				Quiet:    true,
				Out:      out,
				Logger:   logger,
				OnResult: func(p config.Pair, err error) { alerts.Report(p.Name, err) },
			})
		},
		Poll:     *poll,
		Debounce: 2 * time.Second,
		Retry:    10 * time.Second,
		Replaced: replaced,
		Logger:   logger,
	})
	logger.Info("stopping")
	return err
}
