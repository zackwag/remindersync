// Command remindersync syncs Apple Reminders lists to other to-do targets.
//
// So far it can set up sync pairs and poke at Apple Reminders and the
// Skylight API directly; it can't sync yet.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

const usage = `Usage: remindersync <command> [arguments]

Commands:
  config      Add and remove sync pairs
  reminders   Read Apple Reminders lists (macOS only)
  skylight    Log in to Skylight and work with its lists directly

Run "remindersync <command> -h" for details.
`

func main() {
	os.Exit(realMain())
}

// realMain holds main's body so deferred calls run before os.Exit.
func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
