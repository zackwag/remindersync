package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/zackwag/remindersync/internal/credentials"
	"github.com/zackwag/remindersync/skylight"
)

const skylightUsage = `Usage: remindersync skylight <command> [flags]

Commands:
  login                                  Log in and store tokens in the Keychain
  frames                                 List frames on the account
  lists   -frame ID                      List a frame's lists
  items   -frame ID -list ID             List a list's items
  add     -frame ID -list ID LABEL       Add an item
  update  -frame ID -list ID -item ID [-label TEXT] [-status pending|completed]
  delete  -frame ID -list ID -item ID    Delete an item

Every command except login accepts -v to log full request and response bodies.
`

func runSkylight(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, skylightUsage)
		return errUsage
	}

	cmd, args := args[0], args[1:]
	if cmd == "login" {
		return skylightLogin(ctx, bufio.NewReader(os.Stdin))
	}

	fs := flag.NewFlagSet("skylight "+cmd, flag.ContinueOnError)
	verbose := fs.Bool("v", false, "log full request and response bodies")
	frame := fs.String("frame", "", "frame ID")
	list := fs.String("list", "", "list ID")
	item := fs.String("item", "", "list item ID")
	label := fs.String("label", "", "new label")
	status := fs.String("status", "", "new status: pending or completed")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	client, err := newSkylightClient(*verbose)
	if err != nil {
		return err
	}

	need := func(names ...string) error {
		values := map[string]string{"frame": *frame, "list": *list, "item": *item}
		for _, n := range names {
			if values[n] == "" {
				return fmt.Errorf("skylight %s: -%s is required", cmd, n)
			}
		}
		return nil
	}

	switch cmd {
	case "frames":
		frames, err := client.Frames(ctx)
		if err != nil {
			return err
		}
		tw := newTable("ID", "NAME")
		for _, f := range frames {
			_, _ = fmt.Fprintf(tw, "%s\t%s\n", f.ID, f.Name)
		}
		return tw.Flush()

	case "lists":
		if err := need("frame"); err != nil {
			return err
		}
		lists, err := client.Lists(ctx, *frame)
		if err != nil {
			return err
		}
		tw := newTable("ID", "LABEL", "KIND")
		for _, l := range lists {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", l.ID, l.Label, l.Kind)
		}
		return tw.Flush()

	case "items":
		if err := need("frame", "list"); err != nil {
			return err
		}
		items, err := client.ListItems(ctx, *frame, *list)
		if err != nil {
			return err
		}
		tw := newTable("ID", "STATUS", "LABEL", "CREATED")
		for _, it := range items {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.ID, it.Status, it.Label, it.CreatedAt)
		}
		return tw.Flush()

	case "add":
		if err := need("frame", "list"); err != nil {
			return err
		}
		text := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if text == "" {
			return errors.New("skylight add: LABEL is required")
		}
		created, err := client.AddListItem(ctx, *frame, *list, text)
		if errors.Is(err, skylight.ErrItemNotReturned) {
			fmt.Println("Added, but the response didn't include the new item. Run with -v to see the raw response.")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("Added %s (%s)\n", created.ID, created.Status)
		return nil

	case "update":
		if err := need("frame", "list", "item"); err != nil {
			return err
		}
		var upd skylight.ListItemUpdate
		if *label != "" {
			upd.Label = label
		}
		if *status != "" {
			s := skylight.ItemStatus(*status)
			if s != skylight.StatusPending && s != skylight.StatusCompleted {
				return fmt.Errorf("skylight update: -status must be pending or completed, got %q", *status)
			}
			upd.Status = &s
		}
		if upd.Label == nil && upd.Status == nil {
			return errors.New("skylight update: set -label and/or -status")
		}
		updated, err := client.UpdateListItem(ctx, *frame, *list, *item, upd)
		if errors.Is(err, skylight.ErrItemNotReturned) {
			fmt.Println("Updated, but the response didn't include the item. Run with -v to see the raw response.")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("Updated %s: %s (%s)\n", updated.ID, updated.Label, updated.Status)
		return nil

	case "delete":
		if err := need("frame", "list", "item"); err != nil {
			return err
		}
		if err := client.DeleteListItem(ctx, *frame, *list, *item); err != nil {
			return err
		}
		fmt.Printf("Deleted %s\n", *item)
		return nil

	default:
		fmt.Fprintf(os.Stderr, "unknown skylight command %q\n\n%s", cmd, skylightUsage)
		return errUsage
	}
}

// skylightLogin runs the one-time OAuth login. It reads from in rather than
// os.Stdin directly so the config wizard can share its buffered reader.
func skylightLogin(ctx context.Context, in *bufio.Reader) error {
	pkce, err := skylight.NewPKCE()
	if err != nil {
		return err
	}
	fp, err := skylight.NewDeviceFingerprint()
	if err != nil {
		return err
	}
	oauth := skylight.OAuth{DeviceFingerprint: fp}

	fmt.Println("1. Open this URL and log in to Skylight:")
	fmt.Println()
	fmt.Println("  ", oauth.AuthorizeURL(pkce.Challenge))
	fmt.Println()
	fmt.Println("2. You'll land on a Skylight welcome page. Copy the full URL from the")
	fmt.Println("   address bar (it contains ?code=...) and paste it here:")
	fmt.Print("> ")

	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("read authorization code: %w", err)
	}
	code, err := skylight.ExtractCode(line)
	if err != nil {
		return err
	}
	tok, err := oauth.ExchangeCode(ctx, code, pkce.Verifier)
	if err != nil {
		return err
	}
	if err := credentials.SaveSkylight(credentials.Skylight{Token: tok, DeviceFingerprint: fp}); err != nil {
		return err
	}
	fmt.Println("Logged in. Tokens saved to the Keychain.")
	return nil
}

func newSkylightClient(verbose bool) (*skylight.Client, error) {
	creds, err := credentials.LoadSkylight()
	if err != nil {
		return nil, err
	}
	opts := []skylight.Option{
		skylight.WithDeviceFingerprint(creds.DeviceFingerprint),
		skylight.WithTokenRefreshHook(func(tok skylight.Token) {
			creds.Token = tok
			if err := credentials.SaveSkylight(creds); err != nil {
				fmt.Fprintln(os.Stderr, "warning: refreshed Skylight token but couldn't save it:", err)
			}
		}),
	}
	if verbose {
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
		opts = append(opts, skylight.WithLogger(logger))
	}
	return skylight.NewClient(creds.Token, opts...), nil
}

func newTable(headers ...string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// tabwriter buffers until Flush, which reports any write error.
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
	return tw
}
