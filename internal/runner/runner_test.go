package runner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/config"
	"github.com/zackwag/remindersync/internal/engine"
	"github.com/zackwag/remindersync/internal/state"
)

// list is a minimal in-memory engine.Store.
type list struct {
	prefix   string
	items    []engine.Item
	next     int
	itemsErr error
}

func (l *list) add(title string) {
	l.next++
	l.items = append(l.items, engine.Item{ID: fmt.Sprintf("%s%d", l.prefix, l.next), Title: title, CreatedAt: time.Now()})
}

func (l *list) Items(context.Context) ([]engine.Item, error) {
	return slices.Clone(l.items), l.itemsErr
}

func (l *list) Create(_ context.Context, title string) (engine.Item, error) {
	l.add(title)
	return l.items[len(l.items)-1], nil
}

func (l *list) Update(_ context.Context, id string, ch engine.Change) (engine.Item, error) {
	i := slices.IndexFunc(l.items, func(it engine.Item) bool { return it.ID == id })
	if ch.Title != nil {
		l.items[i].Title = *ch.Title
	}
	if ch.Completed != nil {
		l.items[i].Completed = *ch.Completed
	}
	return l.items[i], nil
}

func (l *list) Delete(_ context.Context, id string) error {
	l.items = slices.DeleteFunc(l.items, func(it engine.Item) bool { return it.ID == id })
	return nil
}

type opener struct {
	hubs, targets     map[string]*list
	hubErr, targetErr error
}

func (o *opener) Hub(p config.Pair) (engine.Store, error) {
	if o.hubErr != nil {
		return nil, o.hubErr
	}
	return o.hubs[p.Name], nil
}

func (o *opener) Target(_ context.Context, p config.Pair) (engine.Store, error) {
	if o.targetErr != nil {
		return nil, o.targetErr
	}
	return o.targets[p.Name], nil
}

func newPair(name string, dir engine.Direction) config.Pair {
	return config.Pair{
		ID:        "id-" + name,
		Name:      name,
		Direction: dir,
		Reminders: config.RemindersList{ListID: "R-" + name, ListTitle: name},
		Target:    config.Target{Type: config.TargetSkylight, FrameID: "1", ListID: "S-" + name, Label: name + " list"},
	}
}

type fixture struct {
	cfg  config.Config
	db   *state.DB
	path string
	open *opener
	now  time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{
		cfg:  config.Config{Pairs: []config.Pair{newPair("groceries", engine.TwoWay), newPair("todo", engine.OneWay)}},
		db:   db,
		path: path,
		open: &opener{hubs: map[string]*list{}, targets: map[string]*list{}},
		now:  time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC),
	}
	for _, p := range f.cfg.Pairs {
		f.open.hubs[p.Name] = &list{prefix: "h"}
		f.open.targets[p.Name] = &list{prefix: "t"}
	}
	return f
}

func (f *fixture) sync(t *testing.T, opts Options) (string, error) {
	t.Helper()
	var out strings.Builder
	opts.Out = &out
	opts.Now = func() time.Time { return f.now }
	err := Sync(context.Background(), f.cfg, f.db, f.open, opts)
	return out.String(), err
}

func (f *fixture) status(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	if err := Status(context.Background(), f.cfg, f.db, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output is missing %q:\n%s", w, out)
		}
	}
}

func TestSyncAllPairs(t *testing.T) {
	f := newFixture(t)
	f.open.hubs["groceries"].add("Milk")
	f.open.targets["groceries"].add("Bread")

	out, err := f.sync(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out,
		`groceries: Reminders "groceries" <-> Skylight "groceries list"`,
		`  create "Milk" on target`,
		`  create "Bread" in hub`,
		"  Applied 2 changes.",
		`todo: Reminders "todo" -> Skylight "todo list"`,
		"  Nothing to do.",
	)
	if len(f.open.targets["groceries"].items) != 2 {
		t.Errorf("target = %v", f.open.targets["groceries"].items)
	}

	mustContain(t, f.status(t),
		"  Linked items:       2",
		"  Last sync:          "+f.now.Local().Format("2006-01-02 15:04:05")+", 2 changes",
		"  Linked items:       0",
		", 0 changes",
	)
}

func TestDryRunWritesAndRecordsNothing(t *testing.T) {
	f := newFixture(t)
	f.open.hubs["groceries"].add("Milk")

	out, err := f.sync(t, Options{DryRun: true, Pair: "groceries"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, `  create "Milk" on target`, "  1 change planned. Dry run, so nothing was written.")
	if strings.Contains(out, "todo") {
		t.Errorf("-pair groceries also synced todo:\n%s", out)
	}
	if len(f.open.targets["groceries"].items) != 0 {
		t.Errorf("dry run wrote %v", f.open.targets["groceries"].items)
	}
	mustContain(t, f.status(t), "  Last sync:          never")
}

func TestFailedPairDoesntStopTheOthers(t *testing.T) {
	f := newFixture(t)
	f.open.hubs["groceries"].itemsErr = errors.New("EventKit is unhappy")
	f.open.hubs["todo"].add("Call the plumber")

	out, err := f.sync(t, Options{})
	if err == nil || err.Error() != "1 of 2 pairs failed" {
		t.Fatalf("err = %v", err)
	}
	mustContain(t, out,
		"  failed after 0 of 0 changes: read hub: EventKit is unhappy",
		`  create "Call the plumber" on target`,
	)
	mustContain(t, f.status(t), "failed after 0 of 0 changes: read hub: EventKit is unhappy", ", 1 change")
}

func TestTooManyChangesSuggestsForce(t *testing.T) {
	f := newFixture(t)
	hub := f.open.hubs["groceries"]
	for i := range 8 {
		hub.add(fmt.Sprint("item ", i))
	}
	if _, err := f.sync(t, Options{Pair: "groceries"}); err != nil {
		t.Fatal(err)
	}
	hub.items = hub.items[:1]

	out, err := f.sync(t, Options{Pair: "groceries"})
	if err == nil {
		t.Fatal("want an error")
	}
	mustContain(t, out, "too many destructive changes", "rerun with -force")

	if _, err := f.sync(t, Options{Pair: "groceries", Force: true}); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if n := len(f.open.targets["groceries"].items); n != 1 {
		t.Errorf("target has %d items after the forced sync, want 1", n)
	}
}

func TestOpenErrors(t *testing.T) {
	f := newFixture(t)
	f.open.hubErr = errors.New("no Reminders access")
	out, err := f.sync(t, Options{Pair: "todo"})
	if err == nil {
		t.Fatal("want an error")
	}
	mustContain(t, out, "open Reminders: no Reminders access")

	f.open.hubErr, f.open.targetErr = nil, errors.New("not logged in")
	out, _ = f.sync(t, Options{Pair: "todo"})
	mustContain(t, out, `open Skylight "todo list": not logged in`)
}

func TestRecordRunFailureCountsAsFailure(t *testing.T) {
	f := newFixture(t)
	_ = f.db.Close()
	// The closed database fails the sync itself and the record; both are
	// reported, and the pair counts as failed.
	out, err := f.sync(t, Options{Pair: "todo"})
	if err == nil {
		t.Fatal("want an error")
	}
	mustContain(t, out, "couldn't record this run")
}

func TestPairSelection(t *testing.T) {
	f := newFixture(t)
	if _, err := f.sync(t, Options{Pair: "nope"}); err == nil || !strings.Contains(err.Error(), `no pair named "nope"`) {
		t.Errorf("err = %v", err)
	}
	f.cfg.Pairs = nil
	if _, err := f.sync(t, Options{}); !errors.Is(err, ErrNoPairs) {
		t.Errorf("sync with no pairs: err = %v", err)
	}
	if err := Status(context.Background(), f.cfg, f.db, &strings.Builder{}); !errors.Is(err, ErrNoPairs) {
		t.Errorf("status with no pairs: err = %v", err)
	}
}

func TestStatusShowsUnfinishedCreates(t *testing.T) {
	f := newFixture(t)
	p := f.cfg.Pairs[0]
	if err := f.db.BeginCreate(context.Background(), p.ID, engine.PendingCreate{Side: engine.SideTarget, SourceID: "h1", StartedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	mustContain(t, f.status(t), "  Unfinished creates: 1 (the next sync recovers them)")

	_ = f.db.Close()
	if err := Status(context.Background(), f.cfg, f.db, &strings.Builder{}); err == nil {
		t.Error("want an error from a closed database")
	}
}

func TestDefaultClock(t *testing.T) {
	f := newFixture(t)
	if err := Sync(context.Background(), f.cfg, f.db, f.open, Options{Out: &strings.Builder{}}); err != nil {
		t.Fatal(err)
	}
	st, _ := f.db.Status(context.Background(), f.cfg.Pairs[0].ID)
	if st.LastRun == nil || time.Since(st.LastRun.FinishedAt) > time.Minute {
		t.Errorf("last run = %+v, want one stamped just now", st.LastRun)
	}
}

func TestQuietPrintsOnlyPairsWithNews(t *testing.T) {
	f := newFixture(t)
	f.open.hubs["todo"].add("Call the plumber")

	out, err := f.sync(t, Options{Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "groceries") || !strings.HasPrefix(out, "todo: ") {
		t.Errorf("output:\n%s", out)
	}
	mustContain(t, out, "  Applied 1 change.")

	out, err = f.sync(t, Options{Quiet: true})
	if err != nil || out != "" {
		t.Errorf("a quiet sync with nothing to do printed %q, err %v", out, err)
	}
	// Quiet syncs are still recorded.
	mustContain(t, f.status(t), "  Last sync:          "+f.now.Local().Format("2006-01-02 15:04:05")+", 0 changes")

	f.open.hubs["groceries"].itemsErr = errors.New("EventKit is unhappy")
	out, _ = f.sync(t, Options{Quiet: true})
	mustContain(t, out, "groceries: ", "read hub: EventKit is unhappy")

	_ = f.db.Close()
	out, _ = f.sync(t, Options{Quiet: true, Pair: "todo"})
	mustContain(t, out, "todo: ", "couldn't record this run")
}

func TestStatusShowsDaemon(t *testing.T) {
	alive := true
	orig := processAlive
	processAlive = func(int) bool { return alive }
	t.Cleanup(func() { processAlive = orig })

	f := newFixture(t)
	since := f.now.Local().Format("2006-01-02 15:04:05")
	mustContain(t, f.status(t), "Daemon: never started")

	for _, tc := range []struct {
		state state.DaemonState
		alive bool
		want  string
	}{
		{state.DaemonRunning, true, "Daemon: running since " + since + " (pid 42)"},
		{state.DaemonWaitingForAccess, true, "Daemon: waiting for Reminders access since " + since + " (pid 42). Click Allow"},
		{state.DaemonRunning, false, "Daemon: not running. It stopped without exiting cleanly some time after " + since},
		{state.DaemonWaitingForAccess, false, "Daemon: not running."},
		{state.DaemonAccessDenied, false, "Daemon: stopped at " + since + " because Reminders access was denied"},
		{state.DaemonStopped, false, "Daemon: stopped at " + since + "\n"},
	} {
		alive = tc.alive
		if err := f.db.SetDaemon(context.Background(), state.Daemon{PID: 42, State: tc.state, Since: f.now}); err != nil {
			t.Fatal(err)
		}
		mustContain(t, f.status(t), tc.want)
	}

	// A pair whose status can't be read fails status.
	if _, err := f.sync(t, Options{Pair: "todo"}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE run SET finished_at = 'whenever'`); err != nil {
		t.Fatal(err)
	}
	if err := Status(context.Background(), f.cfg, f.db, &strings.Builder{}); err == nil {
		t.Error("want an error for a bad run time")
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this process isn't alive")
	}
	// Pids are capped well below this.
	if processAlive(1 << 30) {
		t.Error("a pid that can't exist is alive")
	}
}
