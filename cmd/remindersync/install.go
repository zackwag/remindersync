package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/credentials"
	"github.com/zackwag/remindersync/internal/launchd"
	"github.com/zackwag/remindersync/internal/notify"
	"github.com/zackwag/remindersync/internal/runner"
	"github.com/zackwag/remindersync/internal/state"
)

// installPaths are where install puts things.
type installPaths struct {
	bin, plist, log string
}

func defaultInstallPaths() (installPaths, error) {
	confDir, err := os.UserConfigDir()
	if err != nil {
		return installPaths{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return installPaths{}, err
	}
	return installPaths{
		bin:   filepath.Join(confDir, "remindersync", "bin", "remindersync"),
		plist: filepath.Join(home, "Library", "LaunchAgents", launchd.Label+".plist"),
		log:   filepath.Join(home, "Library", "Logs", "remindersync", "daemon.log"),
	}, nil
}

func runInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: remindersync install [flags]\n\nRun `remindersync daemon` as a launchd agent that starts at login. Run from\nReminderSync.app, the agent runs the app's own binary; otherwise this binary\nis copied into place first. Run it again after upgrading.\n\n")
		fs.PrintDefaults()
	}
	var p paths
	if err := p.register(fs); err != nil {
		return err
	}
	poll := fs.Duration("poll", defaultPoll, "how often the daemon checks targets for changes")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the daemon to report it's running")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *poll < minPoll {
		return fmt.Errorf("-poll must be at least %s", minPoll)
	}

	cfg, err := config.Load(p.config)
	if err != nil {
		return err
	}
	if len(cfg.Pairs) == 0 {
		return runner.ErrNoPairs
	}
	// Skylight is the only target, so every pair needs its login.
	if _, err := credentials.LoadSkylight(); err != nil {
		return err
	}
	ip, err := defaultInstallPaths()
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, p.state)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	prev, err := db.Daemon(ctx)
	if err != nil {
		return err
	}

	exe, bundle, err := installExecutable(ip.bin)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ip.log), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ip.plist), 0o755); err != nil {
		return err
	}
	agent := launchd.Agent{
		Label:   launchd.Label,
		Program: []string{exe, "daemon", "-config", p.config, "-state", p.state, "-poll", poll.String()},
		LogPath: ip.log,
	}
	if bundle != "" {
		agent.BundleID = launchd.BundleID
	}
	if err := os.WriteFile(ip.plist, agent.Plist(), 0o644); err != nil {
		return err
	}
	if err := launchd.Load(launchd.Exec, ip.plist, launchd.Label); err != nil {
		return err
	}
	if bundle != "" {
		fmt.Printf("Loaded %s,\nwhich runs %s.\n", ip.plist, bundle)
	} else {
		fmt.Printf("Copied remindersync to %s\nand loaded %s.\n", ip.bin, ip.plist)
	}
	if err := waitForDaemon(ctx, db, prev, *wait, ip.log); err != nil {
		return err
	}
	if bundle != "" {
		askToNotify()
	}
	return nil
}

// askToNotify asks for permission to show notifications, which the daemon
// uses to say a pair keeps failing. Asking here rather than from the daemon
// means the prompt appears while someone's at the Mac. The answer applies to
// the app, so it covers the daemon too.
func askToNotify() {
	fmt.Println("Asking whether ReminderSync can send notifications, to tell you when a pair keeps failing.")
	p, err := notify.Authorize(time.Minute)
	switch {
	case err != nil:
		fmt.Printf("Couldn't ask: %v\n", err)
	case p == notify.Allowed:
		fmt.Println("Notifications are on.")
	case p == notify.Denied:
		fmt.Println("Notifications are off, so failures only show in the log and `remindersync status`.\nTurn them on in System Settings > Notifications > ReminderSync.")
	default:
		fmt.Println("No answer yet. macOS asks with a notification, which is easy to miss, so check\nSystem Settings > Notifications > ReminderSync and turn on Allow notifications.")
	}
}

// waitForDaemon waits for the newly loaded daemon to report that it's running,
// telling the user to click Allow if it's waiting on the Reminders prompt.
// Reports from the daemon that was running before, such as the "stopped" it
// writes when launchd replaces it, are told apart by PID.
func waitForDaemon(ctx context.Context, db *state.DB, prev *state.Daemon, timeout time.Duration, logPath string) error {
	stale := func(d *state.Daemon) bool {
		return d == nil || (prev != nil && d.PID == prev.PID)
	}
	deadline := time.Now().Add(timeout)
	asked := false
	for {
		d, err := db.Daemon(ctx)
		if err != nil {
			return err
		}
		if !stale(d) {
			switch d.State {
			case state.DaemonRunning:
				fmt.Printf("The daemon is running (pid %d). It logs to %s,\nand `remindersync status` shows how it's doing.\n", d.PID, logPath)
				return nil
			case state.DaemonWaitingForAccess:
				if !asked {
					fmt.Println("macOS should be asking whether remindersync can access your reminders. Click Allow.")
					asked = true
				}
			case state.DaemonAccessDenied:
				return errors.New("reminders access was denied. Allow remindersync in System Settings > Privacy & Security > Reminders, then run `remindersync install` again")
			case state.DaemonStopped:
				return fmt.Errorf("the daemon stopped right after starting; see %s", logPath)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the daemon hasn't reported running after %s; see %s and `launchctl print %s/%s`", timeout, logPath, launchd.Domain(), launchd.Label)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// installExecutable returns the binary the agent should run, and the app
// bundle it's in, if any. Run from ReminderSync.app (including through the
// Homebrew symlink), that's the app's own binary: an upgrade replaces the app
// in place under the same signature, so the Reminders permission carries
// over. A copy left in bin by an earlier install is removed. Run from a plain
// build, the binary is copied to bin, so the agent's path stays put however
// it was built.
func installExecutable(bin string) (exe, bundle string, err error) {
	src, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return "", "", err
	}
	if app, ok := launchd.InBundle(src); ok {
		if err := os.Remove(bin); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		return src, app, nil
	}
	return bin, "", copyExecutable(src, bin)
}

// copyExecutable copies src to dst, unless it's already running from there.
// It writes a temporary file and renames it into place, so a daemon running
// the old copy keeps working until it's replaced.
func copyExecutable(src, dst string) error {
	if cur, err := filepath.EvalSymlinks(dst); err == nil && cur == src {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func runUninstall(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: remindersync uninstall\n\nStop the daemon and remove its launchd agent and any binary install copied.\nReminderSync.app, pairs, sync state and the log are kept.\n")
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	ip, err := defaultInstallPaths()
	if err != nil {
		return err
	}
	loaded, err := launchd.Unload(launchd.Exec, launchd.Label)
	if err != nil {
		return err
	}
	if loaded {
		fmt.Println("Stopped the daemon.")
	} else {
		fmt.Println("The daemon wasn't loaded.")
	}
	for _, path := range []string{ip.plist, ip.bin} {
		err := os.Remove(path)
		switch {
		case err == nil:
			fmt.Println("Removed", path)
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	fmt.Printf("Kept your pairs and sync state, so `remindersync sync` still works, and the log at %s.\n", ip.log)
	return nil
}
