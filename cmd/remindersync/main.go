// Command remindersync syncs Apple Reminders lists to other to-do targets.
//
// It sets up sync pairs, syncs them once on demand, and has subcommands for
// poking at Apple Reminders and the Skylight API directly.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/zackwag/remindersync/internal/launchd"
	"github.com/zackwag/remindersync/internal/mainloop"
)

const usage = `Usage: remindersync <command> [arguments]

Commands:
  config      Add and remove sync pairs
  sync        Sync every pair once (-dry-run to preview)
  status      Show the daemon and each pair's linked items and last sync
  daemon      Sync whenever Reminders changes, and poll targets
  install     Run the daemon at login with launchd
  uninstall   Stop the daemon and remove it from launchd
  reminders   Read Apple Reminders lists (macOS only)
  skylight    Log in to Skylight and work with its lists directly

Run "remindersync <command> -h" for details.
`

func main() {
	reexecInBundle()
	// The main thread runs the run loop that EventKit needs to report
	// changes made by other apps, so the command runs on another goroutine.
	mainloop.Run(realMain)
}

// reexecInBundle replaces the process with the app's own binary when it was
// started through a symlink into an app bundle, such as Homebrew's
// /opt/homebrew/bin/remindersync. macOS finds a process's app from the path
// it was started by, so through the symlink, notifications would see no app.
// The re-exec'd process is started by the real path, so it doesn't loop.
func reexecInBundle() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	target, err := filepath.EvalSymlinks(exe)
	if err != nil || target == exe {
		return
	}
	if _, ok := launchd.InBundle(target); !ok {
		return
	}
	// Only returns if it fails, and then running from the symlink still
	// works apart from notifications.
	_ = syscall.Exec(target, os.Args, os.Environ())
}

// realMain holds main's body so deferred calls run before os.Exit.
func realMain() int {
	// launchd stops the daemon with SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, "remindersync:", err)
		}
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "config":
		return runConfig(ctx, args[1:])
	case "sync":
		return runSync(ctx, args[1:])
	case "status":
		return runStatus(ctx, args[1:])
	case "daemon":
		return runDaemon(ctx, args[1:])
	case "install":
		return runInstall(ctx, args[1:])
	case "uninstall":
		return runUninstall(ctx, args[1:])
	case "reminders":
		return runReminders(ctx, args[1:])
	case "skylight":
		return runSkylight(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return errUsage
	}
}
