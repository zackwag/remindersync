package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errInjected = errors.New("injected failure")

func TestSyncDefaultsNowAndLogger(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)

	if _, err := Sync(context.Background(), f.pair, f.state, Options{}); err != nil {
		t.Fatal(err)
	}
	links := f.links(t)
	if len(links) != 1 || links[0].SyncedAt.IsZero() {
		t.Errorf("links = %v, want one with SyncedAt set from the real clock", links)
	}
}

func TestSyncReadErrors(t *testing.T) {
	tests := []struct {
		name   string
		inject func(f *fixture)
	}{
		{"hub items", func(f *fixture) { f.hub.itemsErr = errInjected }},
		{"target items", func(f *fixture) { f.target.itemsErr = errInjected }},
		{"links", func(f *fixture) { f.state.fail["Links"] = errInjected }},
		{"pending creates", func(f *fixture) { f.state.fail["Pending"] = errInjected }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(TwoWay)
			f.hub.add("Milk", false)
			f.target.add("Bread", false)
			tt.inject(f)

			_, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now})
			if !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			if len(f.hub.writes)+len(f.target.writes) != 0 {
				t.Errorf("wrote despite the failed read: hub %v, target %v", f.hub.writes, f.target.writes)
			}
		})
	}
}

// TestSyncWriteFailureResumes fails one write part way through a sync, then
// checks that the next sync finishes the job without duplicating or losing
// anything, and then settles.
func TestSyncWriteFailureResumes(t *testing.T) {
	tests := []struct {
		name string
		dir  Direction
		// change runs after a clean first sync of one "Milk" item, and sets
		// up the change the failing sync should carry.
		change func(t *testing.T, f *fixture)
		inject func(f *fixture)
		// check runs after the recovery sync.
		check func(t *testing.T, f *fixture)
	}{
		{
			name:   "hub update",
			dir:    TwoWay,
			change: func(t *testing.T, f *fixture) { f.target.set(t, f.target.items[0].ID, "Milk", true) },
			inject: func(f *fixture) { f.hub.updateErr = errInjected },
			check:  wantHub("Milk", true),
		},
		{
			name:   "target update",
			dir:    TwoWay,
			change: func(t *testing.T, f *fixture) { f.hub.set(t, f.hub.items[0].ID, "Oat milk", false) },
			inject: func(f *fixture) { f.target.updateErr = errInjected },
			check:  wantTarget("Oat milk", false),
		},
		{
			name:   "saving the link after an update",
			dir:    TwoWay,
			change: func(t *testing.T, f *fixture) { f.hub.set(t, f.hub.items[0].ID, "Oat milk", false) },
			inject: func(f *fixture) { f.state.fail["PutLink"] = errInjected },
			check:  wantTarget("Oat milk", false),
		},
		{
			name: "saving the link between an update's writes",
			dir:  TwoWay,
			change: func(t *testing.T, f *fixture) {
				f.hub.set(t, f.hub.items[0].ID, "Milk", true)
				f.target.set(t, f.target.items[0].ID, "Oat milk", false)
			},
			inject: func(f *fixture) { f.state.fail["PutLink"] = errInjected },
			check: func(t *testing.T, f *fixture) {
				wantHub("Oat milk", true)(t, f)
				wantTarget("Oat milk", true)(t, f)
			},
		},
		{
			name:   "target delete",
			dir:    TwoWay,
			change: func(t *testing.T, f *fixture) { f.hub.remove(t, f.hub.items[0].ID) },
			inject: func(f *fixture) { f.target.deleteErr = errInjected },
			check:  wantEmpty,
		},
		{
			name:   "dropping the link after a delete",
			dir:    TwoWay,
			change: func(t *testing.T, f *fixture) { f.hub.remove(t, f.hub.items[0].ID) },
			inject: func(f *fixture) { f.state.fail["DeleteLink"] = errInjected },
			check:  wantEmpty,
		},
		{
			name:   "completing the hub item",
			dir:    TwoWay,
			change: func(t *testing.T, f *fixture) { f.target.remove(t, f.target.items[0].ID) },
			inject: func(f *fixture) { f.hub.updateErr = errInjected },
			check:  wantHub("Milk", true),
		},
		{
			name:   "journaling a create",
			dir:    OneWay,
			change: func(_ *testing.T, f *fixture) { f.hub.add("Eggs", false) },
			inject: func(f *fixture) { f.state.fail["BeginCreate"] = errInjected },
			check:  wantTargetCount("Eggs", 1),
		},
		{
			name:   "saving the link after a create",
			dir:    OneWay,
			change: func(_ *testing.T, f *fixture) { f.hub.add("Eggs", false) },
			inject: func(f *fixture) { f.state.fail["FinishCreate"] = errInjected },
			check:  wantTargetCount("Eggs", 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(tt.dir)
			f.hub.add("Milk", false)
			f.sync(t, Options{})
			tt.change(t, f)
			tt.inject(f)

			_, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now})
			if !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected failure", err)
			}

			f.heal()
			f.clock.Advance(time.Minute)
			f.sync(t, Options{})

			tt.check(t, f)
			f.mustSettle(t)
		})
	}
}

func TestSyncFailedRecoveryWrite(t *testing.T) {
	f := newFixture(OneWay)
	f.hub.add("Eggs", false)
	f.target.createErr = errInjected
	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); err == nil {
		t.Fatal("want an error")
	}
	f.target.createErr = nil
	f.state.fail["AbandonCreate"] = errInjected

	_, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now})
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if len(f.target.items) != 0 {
		t.Errorf("target = %v, want nothing created before recovery was saved", f.target.items)
	}
}

func TestSyncClearingCompletedOnTargetUnlinks(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)
	f.sync(t, Options{})

	// Checked off on the frame, synced, then cleared from the frame.
	f.target.set(t, f.target.items[0].ID, "Milk", true)
	f.sync(t, Options{})
	f.target.remove(t, f.target.items[0].ID)
	res := f.sync(t, Options{})

	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionUnlink {
		t.Fatalf("actions = %v, want one unlink", res.Actions)
	}
	if len(f.hub.items) != 1 || !f.hub.items[0].Completed {
		t.Errorf("hub = %v, want Milk kept, completed", f.hub.items)
	}
	if len(f.links(t)) != 0 {
		t.Errorf("links = %v, want none", f.links(t))
	}
	f.mustSettle(t)
}

func TestSyncBothSidesGoneUnlinksWithoutWrites(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)
	f.sync(t, Options{})
	f.hub.remove(t, f.hub.items[0].ID)
	f.target.remove(t, f.target.items[0].ID)
	hubWrites, targetWrites := len(f.hub.writes), len(f.target.writes)

	f.sync(t, Options{})

	if len(f.hub.writes) != hubWrites || len(f.target.writes) != targetWrites {
		t.Errorf("unexpected writes: hub %v, target %v", f.hub.writes[hubWrites:], f.target.writes[targetWrites:])
	}
	if len(f.links(t)) != 0 {
		t.Errorf("links = %v, want none", f.links(t))
	}
}

func TestSyncRecoversHubCreateWhoseResponseWasLost(t *testing.T) {
	f := newFixture(TwoWay)
	f.target.add("Bread", false)
	f.hub.loseCreateResponse = true
	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); !errors.Is(err, errLostResponse) {
		t.Fatalf("err = %v, want the lost response", err)
	}
	f.hub.loseCreateResponse = false
	f.clock.Advance(time.Minute)
	f.sync(t, Options{})

	if n := len(f.hub.byTitle("Bread")); n != 1 {
		t.Errorf("hub has %d Bread, want 1", n)
	}
	if len(f.links(t)) != 1 {
		t.Errorf("links = %v, want 1", f.links(t))
	}
	f.mustSettle(t)
}

func TestApplyRejectsUnknownKind(t *testing.T) {
	f := newFixture(TwoWay)
	s := syncer{pair: f.pair, state: f.state, now: f.clock.Now}
	if err := s.apply(context.Background(), Action{Kind: "bogus"}); err == nil {
		t.Fatal("want an error for an unknown action kind")
	}
}

func TestActionString(t *testing.T) {
	milk := Item{ID: "h1", Title: "Milk"}
	bread := Item{ID: "t1", Title: "Bread"}
	tests := []struct {
		a    Action
		want string
	}{
		{Action{Kind: ActionUpdate, Hub: milk}, `update "Milk": record current values`},
		{
			Action{Kind: ActionUpdate, Hub: milk, HubChange: Change{Completed: ptr(true)}, TargetChange: Change{Title: ptr("Milk")}, Conflict: true},
			`update "Milk": hub completed=true, target title="Milk" (conflict, hub wins)`,
		},
		{
			Action{Kind: ActionUpdate, Hub: milk, TargetChange: Change{Completed: ptr(false), Due: ptr("2026-11-04")}},
			`update "Milk": target completed=false due=2026-11-04`,
		},
		{Action{Kind: ActionAdopt, Hub: milk, Target: milk}, `link existing "Milk" on both sides`},
		{Action{Kind: ActionCreateTarget, Hub: milk}, `create "Milk" on target`},
		{Action{Kind: ActionCreateHub, Target: bread}, `create "Bread" in hub`},
		{Action{Kind: ActionCreateTarget, Hub: Item{Title: "Rent", Due: "2026-11-01"}}, `create "Rent" on target, due 2026-11-01`},
		{Action{Kind: ActionCreateHub, Target: Item{Title: "Rent", Due: "2026-11-01"}}, `create "Rent" in hub, due 2026-11-01`},
		{Action{Kind: ActionRecreateTarget, Hub: Item{Title: "Haircut", Due: "2026-10-28", floating: true}}, `re-create "Haircut" on target, due today (it has no date)`},
		{Action{Kind: ActionRecreateTarget, Hub: milk}, `re-create "Milk" on target`},
		{Action{Kind: ActionDeleteTarget, Target: bread}, `delete "Bread" from target`},
		{Action{Kind: ActionCompleteHub, Hub: milk}, `complete "Milk" in hub (deleted on target)`},
		{Action{Kind: ActionUnlink, Link: Link{Hub: Snapshot{Title: "Milk"}}}, `stop tracking "Milk"`},
		{Action{Kind: "bogus"}, "bogus"},
	}
	for _, tt := range tests {
		if got := tt.a.String(); got != tt.want {
			t.Errorf("got  %s\nwant %s", got, tt.want)
		}
	}
}

// heal clears every injected failure.
func (f *fixture) heal() {
	for _, m := range []*memStore{f.hub, f.target} {
		m.itemsErr, m.createErr, m.updateErr, m.deleteErr = nil, nil, nil, nil
		m.loseCreateResponse = false
	}
	clear(f.state.fail)
}

func wantHub(title string, completed bool) func(*testing.T, *fixture) {
	return func(t *testing.T, f *fixture) {
		t.Helper()
		if len(f.hub.items) != 1 || f.hub.items[0].Title != title || f.hub.items[0].Completed != completed {
			t.Errorf("hub = %v, want one %q completed=%t", f.hub.items, title, completed)
		}
	}
}

func wantTarget(title string, completed bool) func(*testing.T, *fixture) {
	return func(t *testing.T, f *fixture) {
		t.Helper()
		if len(f.target.items) != 1 || f.target.items[0].Title != title || f.target.items[0].Completed != completed {
			t.Errorf("target = %v, want one %q completed=%t", f.target.items, title, completed)
		}
	}
}

func wantTargetCount(title string, n int) func(*testing.T, *fixture) {
	return func(t *testing.T, f *fixture) {
		t.Helper()
		if got := len(f.target.byTitle(title)); got != n {
			t.Errorf("target has %d %q, want %d: %v", got, title, n, f.target.items)
		}
	}
}

func wantEmpty(t *testing.T, f *fixture) {
	t.Helper()
	if len(f.hub.items)+len(f.target.items)+len(f.links(t)) != 0 {
		t.Errorf("hub %v, target %v, links %v; want all empty", f.hub.items, f.target.items, f.links(t))
	}
}
