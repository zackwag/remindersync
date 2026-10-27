// Command remindersync syncs Apple Reminders lists to other to-do targets.
//
// For now it only has the Skylight subcommands used to log in and poke at the
// Skylight API directly.
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
  skylight    Log in to Skylight and work with its lists directly

Run "remindersync skylight -h" for details.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, "remindersync:", err)
		}
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errUsage
	}
	switch args[0] {
	case "skylight":
		return runSkylight(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return errUsage
	}
}
