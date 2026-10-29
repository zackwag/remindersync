package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventkit "github.com/BRO3886/go-eventkit/reminders"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/skylight"
)

type fakeReminders struct {
	lists []eventkit.List
	err   error
}

func (f fakeReminders) Lists() ([]eventkit.List, error) { return f.lists, f.err }

type fakeSkylight struct {
	frames []skylight.Frame
	lists  map[string][]skylight.List
	cats   map[string][]skylight.Category
	// err fails Frames, and listsErr and catsErr the others.
	err, listsErr, catsErr error
}

func (f fakeSkylight) Frames(context.Context) ([]skylight.Frame, error) { return f.frames, f.err }

func (f fakeSkylight) Lists(_ context.Context, frameID string) ([]skylight.List, error) {
	return f.lists[frameID], f.listsErr
}

func (f fakeSkylight) Categories(_ context.Context, frameID string) ([]skylight.Category, error) {
	return f.cats[frameID], f.catsErr
}

var (
	remindersLists = []eventkit.List{
		{ID: "R-PERSONAL", Title: "Personal Reminders", Source: "iCloud"},
		{ID: "R-GROCERIES", Title: "Groceries", Source: "iCloud", IsShared: true, SharedToMe: true},
		{ID: "R-FAMILY", Title: "Family", Source: "iCloud", IsShared: true},
		{ID: "R-WORK", Title: "Work", Source: "Exchange", ReadOnly: true},
	}
	oneFrame = fakeSkylight{
		frames: []skylight.Frame{{ID: "5760005", Name: "South Juniper Street"}},
		lists: map[string][]skylight.List{"5760005": {
			{ID: "7763026", Label: "Grocery List"},
			{ID: "7763027", Label: "To-Do List"},
		}},
	}
	now = time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
)

type run struct {
	path string
	out  string
	err  error
	// opened records which connections the wizard opened.
	opened []string
}

// ids numbers pair IDs across runs, so pairs saved by separate runs in one test
// don't collide.
var ids int

func runWizard(t *testing.T, path, input string, rem fakeReminders, sky fakeSkylight) run {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "config.toml")
	}
	r := run{path: path}
	var out strings.Builder
	w := &Wizard{
		In:         bufio.NewReader(strings.NewReader(input)),
		Out:        &out,
		ConfigPath: path,
		Reminders: func() (RemindersLister, error) {
			r.opened = append(r.opened, "reminders")
			return rem, nil
		},
		Skylight: func(context.Context) (SkylightBrowser, error) {
			r.opened = append(r.opened, "skylight")
			return sky, nil
		},
		Now: func() time.Time { return now },
		NewID: func() string {
			ids++
			return fmt.Sprintf("11111111-1111-4111-8111-%012d", ids)
		},
	}
	r.err = w.Run(context.Background())
	r.out = out.String()
	return r
}

func load(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// cfgID returns the first saved pair's ID, which comes from the counter.
func cfgID(t *testing.T, path string) string {
	t.Helper()
	cfg := load(t, path)
	if len(cfg.Pairs) == 0 {
		t.Fatal("nothing saved")
	}
	if !strings.HasPrefix(cfg.Pairs[0].ID, "11111111-1111-4111-8111-") {
		t.Fatalf("ID %s didn't come from NewID", cfg.Pairs[0].ID)
	}
	return cfg.Pairs[0].ID
}

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

func TestNewTwoWayPair(t *testing.T) {
	r := runWizard(t, "", lines(
		"n",
		"2", // Groceries
		"1", // Grocery List
		"1", // two-way
		"",  // default name
		"",  // save
		"q",
	), fakeReminders{lists: remindersLists}, oneFrame)
	if r.err != nil {
		t.Fatal(r.err)
	}

	want := config.Pair{
		ID:        cfgID(t, r.path),
		Name:      "groceries",
		Direction: engine.TwoWay,
		CreatedAt: now,
		Reminders: config.RemindersList{ListID: "R-GROCERIES", ListTitle: "Groceries"},
		Target:    config.Target{Type: config.TargetSkylight, FrameID: "5760005", ListID: "7763026", Label: "Grocery List"},
	}
	cfg := load(t, r.path)
	if len(cfg.Pairs) != 1 || cfg.Pairs[0] != want {
		t.Errorf("saved %+v\nwant  %+v", cfg.Pairs, want)
	}
	for _, s := range []string{
		"No sync pairs configured.",
		"Groceries (iCloud, shared with you)",
		"Family (iCloud, shared)",
		"Work (Exchange, read-only)",
		"Frame: South Juniper Street",
		`Saved "groceries".`,
		`groceries: Reminders "Groceries" <-> Skylight "Grocery List" (two-way)`,
	} {
		if !strings.Contains(r.out, s) {
			t.Errorf("output is missing %q:\n%s", s, r.out)
		}
	}
}

func TestNewPairRetriesBadInputAndCustomName(t *testing.T) {
	r := runWizard(t, "", lines(
		"n",
		"0", "9", "abc", "1", // only 1-4 are valid
		"2",
		"2", // one-way
		"chores",
		"maybe", "y",
		"q",
	), fakeReminders{lists: remindersLists}, oneFrame)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Count(r.out, "Enter a number from 1 to 4.") != 3 {
		t.Errorf("want three retry prompts:\n%s", r.out)
	}
	p := load(t, r.path).Pairs[0]
	if p.Name != "chores" || p.Direction != engine.OneWay || p.Target.ListID != "7763027" || p.Reminders.ListID != "R-PERSONAL" {
		t.Errorf("saved %+v", p)
	}
}

func TestReadOnlyListIsOneWay(t *testing.T) {
	r := runWizard(t, "", lines("n", "4", "1", "", "", "q"), fakeReminders{lists: remindersLists}, oneFrame)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, `"Work" is read-only, so this pair will be one-way.`) {
		t.Errorf("no read-only note:\n%s", r.out)
	}
	if p := load(t, r.path).Pairs[0]; p.Direction != engine.OneWay {
		t.Errorf("direction = %s", p.Direction)
	}
}

func TestTargetListCanOnlyBeUsedOnce(t *testing.T) {
	first := runWizard(t, "", lines("n", "2", "1", "1", "", "", "q"), fakeReminders{lists: remindersLists}, oneFrame)
	if first.err != nil {
		t.Fatal(first.err)
	}

	r := runWizard(t, first.path, lines(
		"n",
		"2", // Groceries again: sharing a Reminders list is fine
		"1", // Grocery List: taken
		"2", // To-Do List
		"1",
		"groceries", // taken
		"",          // the default, groceries-2
		"",          // save
		"q",
	), fakeReminders{lists: remindersLists}, oneFrame)
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, s := range []string{
		`Grocery List (used by "groceries")`,
		`"groceries" already syncs there`,
		"Name for this pair [groceries-2]",
		`There's already a pair named "groceries".`,
		"Sync pairs:\n  groceries: ",
	} {
		if !strings.Contains(r.out, s) {
			t.Errorf("output is missing %q:\n%s", s, r.out)
		}
	}
	cfg := load(t, r.path)
	if len(cfg.Pairs) != 2 || cfg.Pairs[1].Name != "groceries-2" || cfg.Pairs[1].Target.ListID != "7763027" {
		t.Errorf("saved %+v", cfg.Pairs)
	}
}

// withChores is oneFrame with a chore chart, so the wizard asks whether to
// sync to a list or someone's chores.
func withChores() fakeSkylight {
	sky := oneFrame
	sky.cats = map[string][]skylight.Category{"5760005": {
		{ID: "24197710", Label: "Amelia", ChoreChart: true},
		{ID: "24197547", Label: "Zack", ChoreChart: true},
		{ID: "9", Label: "Not on the chart"},
	}}
	return sky
}

func TestNewChorePair(t *testing.T) {
	r := runWizard(t, "", lines(
		"n",
		"1", // Personal Reminders
		"2", // someone's chores
		"2", // Zack
		"1", // two-way
		"", "", "q",
	), fakeReminders{lists: remindersLists}, withChores())
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Contains(r.out, "Not on the chart") {
		t.Errorf("offered a category that isn't on the chore chart:\n%s", r.out)
	}
	p := load(t, r.path).Pairs[0]
	want := config.Target{Type: config.TargetSkylightChores, FrameID: "5760005", CategoryID: "24197547", Label: "Zack"}
	if p.Target != want || p.Name != "personal-reminders" || p.Direction != engine.TwoWay {
		t.Errorf("saved %+v", p)
	}

	// The same person can't be picked twice, but a list on the same frame
	// can still be.
	r = runWizard(t, r.path, lines("n", "3", "2", "2", "1", "1", "", "", "q"), fakeReminders{lists: remindersLists}, withChores())
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.out, `Zack (used by "personal-reminders")`) {
		t.Errorf("output:\n%s", r.out)
	}
	if p := load(t, r.path).Pairs[1]; p.Target.CategoryID != "24197710" {
		t.Errorf("saved %+v", p.Target)
	}
	r = runWizard(t, r.path, lines("n", "3", "1", "1", "1", "family-list", "", "q"), fakeReminders{lists: remindersLists}, withChores())
	if p := load(t, r.path).Pairs[2]; r.err != nil || p.Target.Type != config.TargetSkylight || p.Target.ListID != "7763026" {
		t.Errorf("saved %+v, err %v", p.Target, r.err)
	}
}

func TestChooseAmongSeveralFrames(t *testing.T) {
	sky := fakeSkylight{
		frames: []skylight.Frame{{ID: "1", Name: "Kitchen"}, {ID: "2", Name: "Cabin"}},
		lists:  map[string][]skylight.List{"2": {{ID: "20", Label: "Cabin groceries"}}},
	}
	r := runWizard(t, "", lines("n", "1", "2", "1", "1", "", "", "q"), fakeReminders{lists: remindersLists}, sky)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if p := load(t, r.path).Pairs[0]; p.Target.FrameID != "2" || p.Target.ListID != "20" {
		t.Errorf("saved %+v", p.Target)
	}
}

func TestDeclineSaveWritesNothing(t *testing.T) {
	r := runWizard(t, "", lines("n", "2", "1", "1", "", "n", "q"), fakeReminders{lists: remindersLists}, oneFrame)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if cfg := load(t, r.path); len(cfg.Pairs) != 0 {
		t.Errorf("saved %+v after declining", cfg.Pairs)
	}
}

func TestDeletePair(t *testing.T) {
	first := runWizard(t, "", lines("n", "2", "1", "1", "", "", "q"), fakeReminders{lists: remindersLists}, oneFrame)

	declined := runWizard(t, first.path, lines("d", "1", "", "q"), fakeReminders{}, fakeSkylight{})
	if declined.err != nil || len(load(t, first.path).Pairs) != 1 {
		t.Fatalf("declining deleted the pair: %v", declined.err)
	}

	r := runWizard(t, first.path, lines("d", "1", "y", "q"), fakeReminders{}, fakeSkylight{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if cfg := load(t, r.path); len(cfg.Pairs) != 0 {
		t.Errorf("pairs = %+v, want none", cfg.Pairs)
	}
	if len(r.opened) != 0 {
		t.Errorf("deleting opened %v; it shouldn't need Reminders or Skylight", r.opened)
	}
}

func TestMenuOnlyOpensConnectionsWhenNeeded(t *testing.T) {
	r := runWizard(t, "", lines("x", "d", "q"), fakeReminders{}, fakeSkylight{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.opened) != 0 {
		t.Errorf("opened %v without starting a new pair", r.opened)
	}
}

func TestEndOfInputQuitsCleanly(t *testing.T) {
	for _, input := range []string{"", "n\n", "n\n2\n1\n1\n"} {
		r := runWizard(t, "", input, fakeReminders{lists: remindersLists}, oneFrame)
		if r.err != nil {
			t.Errorf("input %q: %v", input, r.err)
		}
		if cfg := load(t, r.path); len(cfg.Pairs) != 0 {
			t.Errorf("input %q saved %+v", input, cfg.Pairs)
		}
	}
}

func TestNothingToPickFrom(t *testing.T) {
	r := runWizard(t, "", lines("n", "q"), fakeReminders{}, oneFrame)
	if !strings.Contains(r.out, "No Reminders lists found.") {
		t.Errorf("output:\n%s", r.out)
	}
	r = runWizard(t, "", lines("n", "1", "q"), fakeReminders{lists: remindersLists}, fakeSkylight{})
	if !strings.Contains(r.out, "This Skylight account has no frames.") {
		t.Errorf("output:\n%s", r.out)
	}
	empty := fakeSkylight{frames: oneFrame.frames}
	r = runWizard(t, "", lines("n", "1", "q"), fakeReminders{lists: remindersLists}, empty)
	if !strings.Contains(r.out, "South Juniper Street has no lists.") {
		t.Errorf("output:\n%s", r.out)
	}
}

func TestErrorsStopTheWizard(t *testing.T) {
	boom := errors.New("boom")
	if r := runWizard(t, "", lines("n"), fakeReminders{err: boom}, oneFrame); !errors.Is(r.err, boom) {
		t.Errorf("reminders: err = %v", r.err)
	}
	if r := runWizard(t, "", lines("n", "1"), fakeReminders{lists: remindersLists}, fakeSkylight{err: boom}); !errors.Is(r.err, boom) {
		t.Errorf("skylight: err = %v", r.err)
	}
	sky := withChores()
	sky.catsErr = boom
	if r := runWizard(t, "", lines("n", "1"), fakeReminders{lists: remindersLists}, sky); !errors.Is(r.err, boom) {
		t.Errorf("categories: err = %v", r.err)
	}
	sky = withChores()
	sky.listsErr = boom
	if r := runWizard(t, "", lines("n", "1", "1"), fakeReminders{lists: remindersLists}, sky); !errors.Is(r.err, boom) {
		t.Errorf("lists: err = %v", r.err)
	}
	if r := runWizard(t, "", lines("n", "1"), fakeReminders{lists: remindersLists}, withChores()); r.err != nil {
		t.Errorf("end of input at the target question: err = %v", r.err)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Groceries":       "groceries",
		"Xmas!!! 🎄🎅🏽🎁":    "xmas",
		"Ellen's Travel":  "ellen-s-travel",
		"🍰":               "pair",
		"  To-Do  List  ": "to-do-list",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
