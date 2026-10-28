package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/credentials"
	"github.com/zackwag/remindersync/internal/wizard"
)

func runConfig(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: remindersync config [-path FILE]\n\nAdd and remove sync pairs interactively.\n\n")
		fs.PrintDefaults()
	}
	defPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	path := fs.String("path", defPath, "config file")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	in := bufio.NewReader(os.Stdin)
	fmt.Println("Config file:", *path)
	w := &wizard.Wizard{
		In:         in,
		Out:        os.Stdout,
		ConfigPath: *path,
		Reminders: func() (wizard.RemindersLister, error) {
			c, err := eventkit.New()
			if errors.Is(err, eventkit.ErrAccessDenied) {
				return nil, fmt.Errorf("%w (grant access in System Settings > Privacy & Security > Reminders)", err)
			}
			return c, err
		},
		Skylight: func(ctx context.Context) (wizard.SkylightBrowser, error) {
			c, err := newSkylightClient(false)
			if !errors.Is(err, credentials.ErrNotFound) {
				return c, err
			}
			fmt.Println("\nSkylight isn't logged in yet.")
			if err := skylightLogin(ctx, in); err != nil {
				return nil, err
			}
			return newSkylightClient(false)
		},
	}
	return w.Run(ctx)
}
