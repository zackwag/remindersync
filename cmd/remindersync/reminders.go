package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/BRO3886/go-eventkit/reminders"
)

const remindersUsage = `Usage: remindersync reminders <command> [flags]

Commands:
  lists                                  List Reminders lists across all accounts
  items   -list ID [-completed]          List a list's reminders (incomplete only by default)

The first run asks macOS for Reminders access. The prompt names your terminal
app, not remindersync.
`

func runReminders(_ context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, remindersUsage)
		return errUsage
	}

	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("reminders "+cmd, flag.ContinueOnError)
	list := fs.String("list", "", "list ID")
	completed := fs.Bool("completed", false, "include completed reminders")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	switch cmd {
	case "lists", "items":
	default:
		fmt.Fprintf(os.Stderr, "unknown reminders command %q\n\n%s", cmd, remindersUsage)
		return errUsage
	}

	client, err := reminders.New()
	if errors.Is(err, reminders.ErrAccessDenied) {
		return fmt.Errorf("%w (grant access in System Settings > Privacy & Security > Reminders)", err)
	}
	if err != nil {
		return err
	}

	switch cmd {
	case "lists":
		lists, err := client.Lists()
		if err != nil {
			return err
		}
		tw := newTable("ID", "TITLE", "SOURCE", "COUNT", "READ-ONLY", "SHARED", "SHARED-TO-ME", "OWNED-BY-ME")
		for _, l := range lists {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%t\t%t\t%t\t%t\n",
				l.ID, l.Title, l.Source, l.Count, l.ReadOnly, l.IsShared, l.SharedToMe, l.IsOwnedByMe)
		}
		return tw.Flush()

	default:
		if *list == "" {
			return errors.New("reminders items: -list is required")
		}
		// go-eventkit v0.16.0's WithListID passes the ID to the bridge as a
		// list name, so it never matches. Fetch everything and filter here
		// instead; that also avoids name collisions between lists.
		var opts []reminders.ListOption
		if !*completed {
			opts = append(opts, reminders.WithCompleted(false))
		}
		items, err := client.Reminders(opts...)
		if err != nil {
			return err
		}
		tw := newTable("ID", "COMPLETED", "TITLE", "MODIFIED", "COMPLETED-AT")
		for _, r := range items {
			if r.ListID != *list {
				continue
			}
			_, _ = fmt.Fprintf(tw, "%s\t%t\t%s\t%s\t%s\n",
				r.ID, r.Completed, r.Title, formatTime(r.ModifiedAt), formatTime(r.CompletionDate))
		}
		return tw.Flush()
	}
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}
