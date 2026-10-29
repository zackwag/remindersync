// Package wizard is the interactive `remindersync config` menu for adding and
// removing sync pairs, in the spirit of `rclone config`.
package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/skylight"
)

// errQuit ends the wizard cleanly, on "q" or end of input.
var errQuit = errors.New("quit")

// RemindersLister lists Reminders lists. *eventkit.Client implements it.
type RemindersLister interface {
	Lists() ([]eventkit.List, error)
}

// SkylightBrowser lists Skylight frames, lists and chore categories.
// *skylight.Client implements it.
type SkylightBrowser interface {
	Frames(ctx context.Context) ([]skylight.Frame, error)
	Lists(ctx context.Context, frameID string) ([]skylight.List, error)
	Categories(ctx context.Context, frameID string) ([]skylight.Category, error)
}

// Wizard runs the menu. The Reminders and Skylight connections are opened
// only when a new pair needs them, since the first opens a macOS permission
// prompt and the second may need a login.
type Wizard struct {
	In         *bufio.Reader
	Out        io.Writer
	ConfigPath string
	Reminders  func() (RemindersLister, error)
	Skylight   func(ctx context.Context) (SkylightBrowser, error)
	// Now and NewID default to time.Now and config.NewID.
	Now   func() time.Time
	NewID func() string
}

// Run shows the menu until the user quits.
func (w *Wizard) Run(ctx context.Context) error {
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.NewID == nil {
		w.NewID = config.NewID
	}
	for {
		cfg, err := config.Load(w.ConfigPath)
		if err != nil {
			return err
		}
		w.printf("\n")
		if len(cfg.Pairs) == 0 {
			w.printf("No sync pairs configured.\n")
		} else {
			w.printf("Sync pairs:\n")
			for _, p := range cfg.Pairs {
				w.printf("  %s (%s)\n", p, p.Direction)
			}
		}
		w.printf("\nn) New pair\n")
		if len(cfg.Pairs) > 0 {
			w.printf("d) Delete pair\n")
		}
		w.printf("q) Quit\n")

		choice, err := w.ask("> ")
		if err != nil {
			return quitOK(err)
		}
		switch strings.ToLower(choice) {
		case "n":
			err = w.newPair(ctx, cfg)
		case "d":
			if len(cfg.Pairs) == 0 {
				continue
			}
			err = w.deletePair(cfg)
		case "q":
			return nil
		default:
			continue
		}
		if err != nil {
			return quitOK(err)
		}
	}
}

func (w *Wizard) newPair(ctx context.Context, cfg config.Config) error {
	p := config.Pair{ID: w.NewID(), CreatedAt: w.Now().UTC().Truncate(time.Second)}

	// Reminders list.
	rem, err := w.Reminders()
	if err != nil {
		return err
	}
	lists, err := rem.Lists()
	if err != nil {
		return err
	}
	if len(lists) == 0 {
		w.printf("No Reminders lists found. Is Reminders set up on this Mac?\n")
		return nil
	}
	w.printf("\nReminders lists:\n")
	labels := make([]string, len(lists))
	for i, l := range lists {
		labels[i] = l.Title + describeList(l)
	}
	i, err := w.choose("Pick the Reminders list", labels)
	if err != nil {
		return err
	}
	hub := lists[i]
	p.Reminders = config.RemindersList{ListID: hub.ID, ListTitle: hub.Title}

	// Target. Skylight is the only one so far.
	w.printf("\nTarget: Skylight\n")
	sky, err := w.Skylight(ctx)
	if err != nil {
		return err
	}
	frames, err := sky.Frames(ctx)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		w.printf("This Skylight account has no frames.\n")
		return nil
	}
	frame := frames[0]
	if len(frames) == 1 {
		w.printf("Frame: %s\n", frame.Name)
	} else {
		names := make([]string, len(frames))
		for i, f := range frames {
			names[i] = f.Name
		}
		w.printf("\nFrames:\n")
		i, err := w.choose("Pick a frame", names)
		if err != nil {
			return err
		}
		frame = frames[i]
	}
	cats, err := sky.Categories(ctx, frame.ID)
	if err != nil {
		return err
	}
	var people []skylight.Category
	for _, c := range cats {
		if c.ChoreChart {
			people = append(people, c)
		}
	}
	chores := false
	if len(people) > 0 {
		w.printf("\nSync to:\n")
		i, err := w.choose("Pick what to sync to", []string{
			"A list",
			"Someone's chores: reminders with due dates become chores on those dates",
		})
		if err != nil {
			return err
		}
		chores = i == 1
	}
	var options []config.Target
	if chores {
		w.printf("\nChore chart on %s:\n", frame.Name)
		for _, c := range people {
			options = append(options, config.Target{Type: config.TargetSkylightChores, FrameID: frame.ID, CategoryID: c.ID, Label: c.Label})
		}
	} else {
		skyLists, err := sky.Lists(ctx, frame.ID)
		if err != nil {
			return err
		}
		if len(skyLists) == 0 {
			w.printf("%s has no lists. Add one in the Skylight app first.\n", frame.Name)
			return nil
		}
		w.printf("\nLists on %s:\n", frame.Name)
		for _, l := range skyLists {
			options = append(options, config.Target{Type: config.TargetSkylight, FrameID: frame.ID, ListID: l.ID, Label: l.Label})
		}
	}
	if p.Target, err = w.chooseTarget(cfg, options); err != nil {
		return err
	}

	// Direction.
	if hub.ReadOnly {
		w.printf("\n%q is read-only, so this pair will be one-way.\n", hub.Title)
		p.Direction = engine.OneWay
	} else {
		w.printf("\nDirection:\n")
		i, err := w.choose("Pick a direction", []string{
			"Two-way: changes on either side sync to the other",
			"One-way: Skylight mirrors Reminders, and items added on Skylight are left alone",
		})
		if err != nil {
			return err
		}
		p.Direction = engine.TwoWay
		if i == 1 {
			p.Direction = engine.OneWay
		}
	}

	// Name.
	def := uniqueName(cfg, slug(hub.Title))
	for {
		name, err := w.ask(fmt.Sprintf("\nName for this pair [%s]> ", def))
		if err != nil {
			return err
		}
		if name == "" {
			name = def
		}
		if _, taken := cfg.Find(name); taken {
			w.printf("There's already a pair named %q.\n", name)
			continue
		}
		p.Name = name
		break
	}

	w.printf("\n%s (%s)\n", p, p.Direction)
	if ok, err := w.confirm("Save this pair?", true); err != nil || !ok {
		return err
	}
	cfg.Pairs = append(cfg.Pairs, p)
	if err := config.Save(w.ConfigPath, cfg); err != nil {
		return err
	}
	w.printf("Saved %q.\n", p.Name)
	return nil
}

func (w *Wizard) deletePair(cfg config.Config) error {
	w.printf("\n")
	labels := make([]string, len(cfg.Pairs))
	for i, p := range cfg.Pairs {
		labels[i] = p.String()
	}
	i, err := w.choose("Pick the pair to delete", labels)
	if err != nil {
		return err
	}
	p := cfg.Pairs[i]
	ok, err := w.confirm(fmt.Sprintf("Delete %q? Nothing in Reminders or on Skylight is changed.", p.Name), false)
	if err != nil || !ok {
		return err
	}
	cfg.Pairs = append(cfg.Pairs[:i:i], cfg.Pairs[i+1:]...)
	if err := config.Save(w.ConfigPath, cfg); err != nil {
		return err
	}
	w.printf("Deleted %q.\n", p.Name)
	return nil
}

// chooseTarget asks for one of options, refusing any that another pair
// already syncs to.
func (w *Wizard) chooseTarget(cfg config.Config, options []config.Target) (config.Target, error) {
	labels := make([]string, len(options))
	for i, t := range options {
		labels[i] = t.Label
		if owner := targetOwner(cfg, t); owner != "" {
			labels[i] += fmt.Sprintf(" (used by %q)", owner)
		}
	}
	for {
		i, err := w.choose("Pick the destination", labels)
		if err != nil {
			return config.Target{}, err
		}
		if owner := targetOwner(cfg, options[i]); owner != "" {
			w.printf("%q already syncs there, and two pairs would fight over it. Pick another.\n", owner)
			continue
		}
		return options[i], nil
	}
}

// choose prints numbered options and asks until it gets a valid number.
func (w *Wizard) choose(prompt string, options []string) (int, error) {
	for i, o := range options {
		w.printf("%2d) %s\n", i+1, o)
	}
	for {
		answer, err := w.ask(prompt + "> ")
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(answer)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		w.printf("Enter a number from 1 to %d.\n", len(options))
	}
}

func (w *Wizard) confirm(prompt string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		answer, err := w.ask(fmt.Sprintf("%s %s> ", prompt, hint))
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// ask prints a prompt and reads one trimmed line. End of input is errQuit.
func (w *Wizard) ask(prompt string) (string, error) {
	w.printf("%s", prompt)
	line, err := w.In.ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		w.printf("\n")
		return "", errQuit
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (w *Wizard) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.Out, format, args...)
}

func quitOK(err error) error {
	if errors.Is(err, errQuit) {
		return nil
	}
	return err
}

func describeList(l eventkit.List) string {
	var notes []string
	if l.Source != "" {
		notes = append(notes, l.Source)
	}
	switch {
	case l.SharedToMe:
		notes = append(notes, "shared with you")
	case l.IsShared:
		notes = append(notes, "shared")
	}
	if l.ReadOnly {
		notes = append(notes, "read-only")
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, ", ") + ")"
}

// targetOwner returns the name of the pair already syncing to t, or "".
func targetOwner(cfg config.Config, t config.Target) string {
	for _, p := range cfg.Pairs {
		pt := p.Target
		if pt.Type == t.Type && pt.FrameID == t.FrameID && pt.ListID == t.ListID && pt.CategoryID == t.CategoryID {
			return p.Name
		}
	}
	return ""
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "pair"
	}
	return s
}

func uniqueName(cfg config.Config, base string) string {
	name := base
	for n := 2; ; n++ {
		if _, taken := cfg.Find(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
}
