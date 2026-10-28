package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zackwag/remindersync/internal/engine"
)

func open(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

var synced = time.Date(2026, 10, 28, 13, 11, 4, 198_000_000, time.UTC)

func link(hubID, targetID, title string) engine.Link {
	return engine.Link{
		HubID:    hubID,
		TargetID: targetID,
		Hub:      engine.Snapshot{Title: title},
		Target:   engine.Snapshot{Title: strings.TrimSpace(title), Completed: true},
		SyncedAt: synced,
	}
}

func TestDefaultPath(t *testing.T) {
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, filepath.Join("remindersync", "state.db")) {
		t.Errorf("DefaultPath() = %s", p)
	}
}

func TestOpenPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	db, path := open(t)
	want := link("h1", "t1", "Milk ")
	if err := db.PutLink(ctx, "p", want); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := db.Links(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	db, path := open(t)
	if _, err := db.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v, want a newer-schema error", err)
	}
}

func TestLinks(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	for _, l := range []engine.Link{link("h2", "t2", "Eggs"), link("h1", "t1", "Milk")} {
		if err := db.PutLink(ctx, "p", l); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutLink(ctx, "other", link("h1", "t1", "Milk")); err != nil {
		t.Fatalf("same IDs in another pair: %v", err)
	}

	t.Run("ordered by hub ID and scoped to the pair", func(t *testing.T) {
		got, err := db.Links(ctx, "p")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, l := range got {
			ids = append(ids, l.HubID)
		}
		if !slices.Equal(ids, []string{"h1", "h2"}) {
			t.Errorf("hub IDs = %v", ids)
		}
	})

	t.Run("put replaces by hub ID", func(t *testing.T) {
		repl := link("h1", "t9", "Oat milk")
		if err := db.PutLink(ctx, "p", repl); err != nil {
			t.Fatal(err)
		}
		got, _ := db.Links(ctx, "p")
		if len(got) != 2 || !reflect.DeepEqual(got[0], repl) {
			t.Errorf("got %+v, want h1 replaced with %+v", got, repl)
		}
	})

	t.Run("a target can only be linked once", func(t *testing.T) {
		if err := db.PutLink(ctx, "p", link("h3", "t2", "Eggs again")); err == nil {
			t.Error("want an error linking t2 to a second hub item")
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := db.DeleteLink(ctx, "p", "h2"); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteLink(ctx, "p", "missing"); err != nil {
			t.Errorf("deleting a missing link: %v", err)
		}
		got, _ := db.Links(ctx, "p")
		if len(got) != 1 || got[0].HubID != "h1" {
			t.Errorf("got %+v, want only h1", got)
		}
		other, _ := db.Links(ctx, "other")
		if len(other) != 1 {
			t.Errorf("other pair = %+v, want untouched", other)
		}
	})
}

func TestPendingCreates(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	p := engine.PendingCreate{Side: engine.SideTarget, SourceID: "h1", Title: "Eggs", StartedAt: synced}
	if err := db.BeginCreate(ctx, "p", p); err != nil {
		t.Fatal(err)
	}

	t.Run("begin again replaces", func(t *testing.T) {
		p.StartedAt = synced.Add(time.Minute)
		if err := db.BeginCreate(ctx, "p", p); err != nil {
			t.Fatal(err)
		}
		got, err := db.Pending(ctx, "p")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], p) {
			t.Errorf("got %+v, want %+v", got, p)
		}
	})

	t.Run("finish is atomic", func(t *testing.T) {
		if err := db.PutLink(ctx, "p", link("h9", "t1", "Taken")); err != nil {
			t.Fatal(err)
		}
		// t1 is taken, so the link insert fails and the pending row must
		// survive the rollback.
		if err := db.FinishCreate(ctx, "p", engine.SideTarget, "h1", link("h1", "t1", "Eggs")); err == nil {
			t.Fatal("want an error")
		}
		if got, _ := db.Pending(ctx, "p"); len(got) != 1 {
			t.Errorf("pending = %+v, want the row kept after a failed finish", got)
		}

		if err := db.FinishCreate(ctx, "p", engine.SideTarget, "h1", link("h1", "t2", "Eggs")); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.Pending(ctx, "p"); len(got) != 0 {
			t.Errorf("pending = %+v, want none", got)
		}
		links, _ := db.Links(ctx, "p")
		if len(links) != 2 {
			t.Errorf("links = %+v, want h1 and h9", links)
		}
	})

	t.Run("abandon", func(t *testing.T) {
		q := engine.PendingCreate{Side: engine.SideHub, SourceID: "t5", Title: "Bread", StartedAt: synced}
		if err := db.BeginCreate(ctx, "p", q); err != nil {
			t.Fatal(err)
		}
		if err := db.AbandonCreate(ctx, "p", engine.SideHub, "t5"); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.Pending(ctx, "p"); len(got) != 0 {
			t.Errorf("pending = %+v, want none", got)
		}
		if links, _ := db.Links(ctx, "p"); len(links) != 2 {
			t.Errorf("abandon changed links: %+v", links)
		}
	})
}

func TestClosedDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	_ = db.Close()
	p := engine.PendingCreate{Side: engine.SideHub, SourceID: "t1", StartedAt: synced}
	errs := map[string]error{}
	_, errs["Links"] = db.Links(ctx, "p")
	errs["PutLink"] = db.PutLink(ctx, "p", link("h1", "t1", "Milk"))
	errs["DeleteLink"] = db.DeleteLink(ctx, "p", "h1")
	_, errs["Pending"] = db.Pending(ctx, "p")
	errs["BeginCreate"] = db.BeginCreate(ctx, "p", p)
	errs["FinishCreate"] = db.FinishCreate(ctx, "p", engine.SideHub, "t1", link("h1", "t1", "Milk"))
	errs["AbandonCreate"] = db.AbandonCreate(ctx, "p", engine.SideHub, "t1")
	for name, err := range errs {
		if err == nil || !strings.HasPrefix(err.Error(), "state: ") {
			t.Errorf("%s on a closed database: err = %v, want a state: error", name, err)
		}
	}
}

// listStore is a minimal in-memory engine.Store, enough to run real syncs
// against the SQLite state.
type listStore struct {
	prefix string
	items  []engine.Item
	next   int
}

func (s *listStore) add(title string) {
	s.next++
	s.items = append(s.items, engine.Item{ID: fmt.Sprintf("%s%d", s.prefix, s.next), Title: title, CreatedAt: time.Now()})
}

func (s *listStore) Items(context.Context) ([]engine.Item, error) { return slices.Clone(s.items), nil }

func (s *listStore) Create(_ context.Context, title string) (engine.Item, error) {
	s.add(title)
	return s.items[len(s.items)-1], nil
}

func (s *listStore) Update(_ context.Context, id string, ch engine.Change) (engine.Item, error) {
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		if ch.Title != nil {
			s.items[i].Title = *ch.Title
		}
		if ch.Completed != nil {
			s.items[i].Completed = *ch.Completed
		}
		return s.items[i], nil
	}
	return engine.Item{}, fmt.Errorf("%s not found", id)
}

func (s *listStore) Delete(_ context.Context, id string) error {
	s.items = slices.DeleteFunc(s.items, func(it engine.Item) bool { return it.ID == id })
	return nil
}

func TestEngineSyncSettlesAcrossReopen(t *testing.T) {
	ctx := context.Background()
	db, path := open(t)
	hub, target := &listStore{prefix: "h"}, &listStore{prefix: "t"}
	hub.add("Milk")
	hub.add("Eggs")
	target.add("Milk")
	target.add("Bread")
	pair := engine.Pair{ID: "groceries", Direction: engine.TwoWay, Hub: hub, Target: target}

	if _, err := engine.Sync(ctx, pair, db, engine.Options{}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// A fresh process with the same database must see nothing left to do.
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	res, err := engine.Sync(ctx, pair, db, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 {
		t.Errorf("second sync planned %v, want nothing", res.Actions)
	}
	if len(hub.items) != 3 || len(target.items) != 3 {
		t.Errorf("hub %v, target %v; want three items each", hub.items, target.items)
	}
}

func TestDefaultPathNeedsHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := DefaultPath(); err == nil {
		t.Error("want an error with no home directory")
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("directory can't be created", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, filepath.Join(file, "state.db")); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("not a database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		if err := os.WriteFile(path, []byte(strings.Repeat("not sqlite ", 100)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, path); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("failed migration rolls back", func(t *testing.T) {
		_, path := open(t)
		orig := migrations
		migrations = append(slices.Clone(orig), "CREATE TABLE broken (;")
		t.Cleanup(func() { migrations = orig })

		if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("version %d", len(migrations))) {
			t.Fatalf("err = %v, want a migration error", err)
		}
		migrations = orig
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("reopen at the old version: %v", err)
		}
		_ = db.Close()
	})
}

func TestCorruptTimesAreErrors(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	if _, err := db.db.ExecContext(ctx, `INSERT INTO link VALUES ('p', 'h1', 't1', 'Milk', 0, 'Milk', 0, 'yesterday')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO pending_create VALUES ('p', 'target', 'h1', 'Milk', 'soon')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Links(ctx, "p"); err == nil {
		t.Error("Links: want an error for a bad synced_at")
	}
	if _, err := db.Pending(ctx, "p"); err == nil {
		t.Error("Pending: want an error for a bad started_at")
	}
}

func TestStatusAndRuns(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)

	st, err := db.Status(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if st != (PairStatus{}) {
		t.Errorf("new pair status = %+v, want zero", st)
	}

	for _, l := range []engine.Link{link("h1", "t1", "Milk"), link("h2", "t2", "Eggs")} {
		if err := db.PutLink(ctx, "p", l); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.BeginCreate(ctx, "p", engine.PendingCreate{Side: engine.SideTarget, SourceID: "h3", StartedAt: synced}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutLink(ctx, "other", link("h1", "t1", "Milk")); err != nil {
		t.Fatal(err)
	}
	first := Run{FinishedAt: synced, Planned: 3, Applied: 1, Error: "skylight: 500"}
	if err := db.RecordRun(ctx, "p", first); err != nil {
		t.Fatal(err)
	}
	second := Run{FinishedAt: synced.Add(time.Minute), Planned: 2, Applied: 2}
	if err := db.RecordRun(ctx, "p", second); err != nil {
		t.Fatal(err)
	}

	st, err = db.Status(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if st.Links != 2 || st.Pending != 1 || st.LastRun == nil || *st.LastRun != second {
		t.Errorf("status = %+v (last run %+v), want 2 links, 1 pending, last run %+v", st, st.LastRun, second)
	}
}

func TestStatusErrors(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	if _, err := db.db.ExecContext(ctx, `INSERT INTO run VALUES ('p', 'whenever', 0, 0, '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Status(ctx, "p"); err == nil {
		t.Error("want an error for a bad finished_at")
	}

	_ = db.Close()
	if _, err := db.Status(ctx, "p"); err == nil {
		t.Error("Status on a closed database: want an error")
	}
	if err := db.RecordRun(ctx, "p", Run{}); err == nil {
		t.Error("RecordRun on a closed database: want an error")
	}
}

func TestUpgradeFromFirstSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	// Build a database as the first release would have left it.
	orig := migrations
	migrations = orig[:1]
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutLink(ctx, "p", link("h1", "t1", "Milk")); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	migrations = orig

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RecordRun(ctx, "p", Run{FinishedAt: synced}); err != nil {
		t.Fatalf("run table missing after upgrade: %v", err)
	}
	if links, _ := db.Links(ctx, "p"); len(links) != 1 {
		t.Errorf("links after upgrade = %+v", links)
	}
}

func TestDaemonState(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	if d, err := db.Daemon(ctx); err != nil || d != nil {
		t.Fatalf("before the daemon ever ran: %+v, %v", d, err)
	}

	waiting := Daemon{PID: 42, State: DaemonWaitingForAccess, Since: synced}
	running := Daemon{PID: 42, State: DaemonRunning, Since: synced.Add(time.Second)}
	for _, want := range []Daemon{waiting, running} {
		if err := db.SetDaemon(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := db.Daemon(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || *got != want {
			t.Errorf("daemon = %+v, want %+v", got, want)
		}
	}
}

func TestDaemonStateErrors(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	if _, err := db.db.ExecContext(ctx, `INSERT INTO daemon VALUES (1, 42, 'running', 'at some point')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Daemon(ctx); err == nil {
		t.Error("want an error for a bad since")
	}

	_ = db.Close()
	if _, err := db.Daemon(ctx); err == nil {
		t.Error("Daemon on a closed database: want an error")
	}
	if err := db.SetDaemon(ctx, Daemon{}); err == nil {
		t.Error("SetDaemon on a closed database: want an error")
	}
}
