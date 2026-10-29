package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	clock  *fakeClock
	hub    *memStore
	target *memStore
	state  *memState
	pair   Pair
}

func newFixture(dir Direction) *fixture {
	clock := newClock()
	f := &fixture{
		clock:  clock,
		hub:    newMemStore("h", clock),
		target: newMemStore("t", clock),
		state:  newMemState(),
	}
	// Reminders keeps due dates. Targets do only in date tests.
	f.hub.dates = true
	f.pair = Pair{ID: "p", Direction: dir, Hub: f.hub, Target: f.target, DaysAhead: MaxDaysAhead}
	return f
}

// newDateFixture is a pair whose target keeps dates, like Skylight chores.
func newDateFixture(dir Direction) *fixture {
	f := newFixture(dir)
	f.target.dates = true
	f.pair.Dates = true
	return f
}

func (f *fixture) sync(t *testing.T, opts Options) Result {
	t.Helper()
	opts.Now = f.clock.Now
	res, err := Sync(context.Background(), f.pair, f.state, opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	f.clock.Advance(time.Minute)
	return res
}

// mustSettle asserts that a sync right after the last one plans nothing.
func (f *fixture) mustSettle(t *testing.T) {
	t.Helper()
	if res := f.sync(t, Options{}); len(res.Actions) != 0 {
		t.Fatalf("sync didn't settle; next run plans %v", res.Actions)
	}
}

func (f *fixture) links(t *testing.T) []Link {
	t.Helper()
	links, err := f.state.Links(context.Background(), f.pair.ID)
	if err != nil {
		t.Fatal(err)
	}
	return links
}

func titles(items []Item) map[string]bool {
	m := make(map[string]bool)
	for _, it := range items {
		m[it.Title] = !it.Completed
	}
	return m
}

func TestSyncFirstRunMergesBothListsOnce(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)
	f.hub.add("Eggs", false)
	f.hub.add("Old receipt", true)
	f.target.add("Milk", false)
	f.target.add("Bread", false)

	f.sync(t, Options{})

	if got := len(f.links(t)); got != 3 {
		t.Errorf("links = %d, want 3", got)
	}
	if n := len(f.target.byTitle("Milk")); n != 1 {
		t.Errorf("target has %d Milk, want 1", n)
	}
	if len(f.target.byTitle("Eggs")) != 1 || len(f.hub.byTitle("Bread")) != 1 {
		t.Errorf("hub %v, target %v", f.hub.items, f.target.items)
	}
	if len(f.target.byTitle("Old receipt")) != 0 {
		t.Error("completed hub item was copied to the target")
	}
	f.mustSettle(t)
}

func TestSyncPropagatesEditsBothWays(t *testing.T) {
	f := newFixture(TwoWay)
	milk := f.hub.add("Milk", false)
	f.sync(t, Options{})
	tMilk := f.target.byTitle("Milk")[0]

	f.hub.set(t, milk.ID, "Oat milk", false)
	f.target.set(t, tMilk.ID, "Milk", true)
	f.sync(t, Options{})

	if got := f.target.items[0]; got.Title != "Oat milk" || !got.Completed {
		t.Errorf("target = %+v, want Oat milk, completed", got)
	}
	if got := f.hub.items[0]; got.Title != "Oat milk" || !got.Completed {
		t.Errorf("hub = %+v, want Oat milk, completed", got)
	}
	f.mustSettle(t)
}

func TestSyncNormalizingTargetDoesNotLoop(t *testing.T) {
	f := newFixture(TwoWay)
	f.target.normalize = strings.TrimSpace
	milk := f.hub.add("Milk  ", false)

	f.sync(t, Options{})
	if got := f.target.items[0].Title; got != "Milk" {
		t.Fatalf("target title = %q, want the normalized %q", got, "Milk")
	}
	f.mustSettle(t)

	f.hub.set(t, milk.ID, "Milk  ", true)
	res := f.sync(t, Options{})
	want := `update "Milk  ": target completed=true`
	if len(res.Actions) != 1 || res.Actions[0].String() != want {
		t.Errorf("actions = %v, want [%s]", res.Actions, want)
	}
	f.mustSettle(t)
}

func TestSyncDeletes(t *testing.T) {
	t.Run("hub delete deletes target item", func(t *testing.T) {
		f := newFixture(TwoWay)
		milk := f.hub.add("Milk", false)
		f.sync(t, Options{})

		f.hub.remove(t, milk.ID)
		f.sync(t, Options{})

		if len(f.target.items) != 0 || len(f.links(t)) != 0 {
			t.Errorf("target %v, links %v; want both empty", f.target.items, f.links(t))
		}
		f.mustSettle(t)
	})

	t.Run("target delete completes the reminder on two-way", func(t *testing.T) {
		f := newFixture(TwoWay)
		f.hub.add("Milk", false)
		f.sync(t, Options{})

		f.target.remove(t, f.target.items[0].ID)
		f.sync(t, Options{})

		if len(f.hub.items) != 1 || !f.hub.items[0].Completed {
			t.Errorf("hub = %v, want Milk kept and completed", f.hub.items)
		}
		if len(f.target.items) != 0 {
			t.Errorf("target = %v, want nothing re-created", f.target.items)
		}
		f.mustSettle(t)
	})

	t.Run("target delete is re-created on one-way", func(t *testing.T) {
		f := newFixture(OneWay)
		f.hub.add("Milk", false)
		f.sync(t, Options{})
		old := f.target.items[0].ID

		f.target.remove(t, old)
		f.sync(t, Options{})

		if len(f.target.items) != 1 || f.target.items[0].ID == old {
			t.Errorf("target = %v, want a new Milk", f.target.items)
		}
		f.mustSettle(t)
	})
}

func TestSyncReadErrorWritesNothing(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)
	f.sync(t, Options{})
	f.target.itemsErr = errors.New("503")

	_, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now})
	if err == nil {
		t.Fatal("want an error")
	}
	if len(f.links(t)) != 1 || len(f.hub.writes) != 0 {
		t.Errorf("links %v, hub writes %v; want state and hub untouched", f.links(t), f.hub.writes)
	}
}

func TestSyncDestructiveChangeLimit(t *testing.T) {
	f := newFixture(TwoWay)
	for _, title := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		f.hub.add(title, false)
	}
	f.sync(t, Options{})
	targetWrites := len(f.target.writes)
	for _, it := range slices.Clone(f.hub.items[:6]) {
		f.hub.remove(t, it.ID)
	}

	_, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now})
	if !errors.Is(err, ErrTooManyChanges) {
		t.Fatalf("err = %v, want ErrTooManyChanges", err)
	}
	if len(f.target.writes) != targetWrites {
		t.Fatalf("target was written to despite the limit: %v", f.target.writes[targetWrites:])
	}

	f.sync(t, Options{Force: true})
	if len(f.target.items) != 2 {
		t.Errorf("target = %v, want 2 left after a forced sync", f.target.items)
	}
}

func TestSyncLimitAllowsFiveOnSmallLists(t *testing.T) {
	f := newFixture(TwoWay)
	for _, title := range []string{"a", "b", "c", "d", "e"} {
		f.hub.add(title, false)
	}
	f.sync(t, Options{})
	for _, it := range slices.Clone(f.target.items) {
		f.target.remove(t, it.ID)
	}

	// Clearing a five-item list on the frame is ordinary use, not a disaster.
	f.sync(t, Options{})
	for _, it := range f.hub.items {
		if !it.Completed {
			t.Errorf("%q not completed", it.Title)
		}
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)
	f.target.add("Bread", false)

	res := f.sync(t, Options{DryRun: true})

	if len(res.Actions) != 2 || res.Applied != 0 {
		t.Errorf("actions %v, applied %d; want 2 planned, 0 applied", res.Actions, res.Applied)
	}
	if len(f.hub.writes)+len(f.target.writes) != 0 || len(f.links(t)) != 0 {
		t.Errorf("dry run wrote: hub %v, target %v, links %v", f.hub.writes, f.target.writes, f.links(t))
	}
}

func TestSyncRecoversCreateWhoseResponseWasLost(t *testing.T) {
	f := newFixture(OneWay)
	eggs := f.hub.add("Eggs", false)
	f.target.loseCreateResponse = true

	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); !errors.Is(err, errLostResponse) {
		t.Fatalf("err = %v, want the lost response", err)
	}
	f.target.loseCreateResponse = false
	f.clock.Advance(time.Minute)

	// The hub item was renamed before the next sync. Recovery must still link
	// the orphan, and the rename must still reach the target.
	f.hub.set(t, eggs.ID, "Brown eggs", false)
	f.sync(t, Options{})

	if len(f.target.items) != 1 || f.target.items[0].Title != "Brown eggs" {
		t.Errorf("target = %v, want exactly one item, renamed to Brown eggs", f.target.items)
	}
	if pending, _ := f.state.Pending(context.Background(), f.pair.ID); len(pending) != 0 {
		t.Errorf("pending = %v, want none", pending)
	}
	f.mustSettle(t)
}

func TestSyncRetriesCreateThatFailed(t *testing.T) {
	f := newFixture(OneWay)
	f.hub.add("Eggs", false)
	f.target.createErr = errors.New("422")

	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); err == nil {
		t.Fatal("want an error")
	}
	f.target.createErr = nil
	f.clock.Advance(time.Minute)
	f.sync(t, Options{})

	if len(f.target.items) != 1 {
		t.Errorf("target = %v, want exactly one Eggs", f.target.items)
	}
	f.mustSettle(t)
}

func TestSyncRecoveryIgnoresOlderItemsWithTheSameTitle(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.add("Milk", false)
	f.sync(t, Options{})

	// An unrelated "Eggs" was already on the target, unlinked, before the
	// interrupted create started.
	oldEggs := f.target.add("Eggs", true)
	f.clock.Advance(10 * time.Minute)
	f.hub.add("Eggs", false)
	f.target.loseCreateResponse = true
	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); err == nil {
		t.Fatal("want an error")
	}
	f.target.loseCreateResponse = false
	f.clock.Advance(time.Minute)
	f.sync(t, Options{})

	for _, l := range f.links(t) {
		if l.TargetID == oldEggs.ID {
			t.Fatalf("recovery linked the pre-existing %q", oldEggs.Title)
		}
	}
	if n := len(f.target.byTitle("Eggs")); n != 2 {
		t.Errorf("target has %d Eggs, want the old completed one plus ours", n)
	}
	f.mustSettle(t)
}

func TestSyncNormalizingHubRewritesTarget(t *testing.T) {
	f := newFixture(TwoWay)
	f.hub.normalize = strings.TrimSpace
	f.hub.add("Milk", false)
	f.sync(t, Options{})

	f.target.set(t, f.target.items[0].ID, "Oat milk  ", false)
	f.sync(t, Options{})
	wantHub("Oat milk", false)(t, f)
	wantTarget("Oat milk", false)(t, f)
	f.mustSettle(t)
}

func TestSyncRepeatingHubItem(t *testing.T) {
	setup := func(t *testing.T) (*fixture, Item) {
		t.Helper()
		f := newFixture(TwoWay)
		mortgage := f.hub.add("Pay Mortgage", false)
		f.hub.repeating = map[string]bool{mortgage.ID: true}
		f.sync(t, Options{})
		return f, f.target.byTitle("Pay Mortgage")[0]
	}

	t.Run("checked off on target reopens it", func(t *testing.T) {
		f, tm := setup(t)
		f.target.set(t, tm.ID, "Pay Mortgage", true)
		f.target.writes = nil

		res := f.sync(t, Options{})
		want := `update "Pay Mortgage": hub completed=true`
		if len(res.Actions) != 1 || res.Actions[0].String() != want {
			t.Errorf("actions = %v, want [%s]", res.Actions, want)
		}
		if n := len(f.hub.byTitle("Pay Mortgage")); n != 2 {
			t.Errorf("hub has %d Pay Mortgage, want the open one and a completed copy", n)
		}
		if f.hub.items[0].Completed {
			t.Error("the repeating hub item was completed")
		}
		if f.target.items[0].Completed {
			t.Error("target item is still checked off")
		}
		if want := []string{"update " + tm.ID + " completed=false"}; !slices.Equal(f.target.writes, want) {
			t.Errorf("target writes = %v, want %v", f.target.writes, want)
		}
		f.mustSettle(t)
	})

	t.Run("deleted on target comes back", func(t *testing.T) {
		f, tm := setup(t)
		f.target.remove(t, tm.ID)

		res := f.sync(t, Options{})
		if len(res.Actions) != 1 || res.Actions[0].Kind != ActionCompleteHub {
			t.Errorf("actions = %v, want one complete-hub", res.Actions)
		}
		res = f.sync(t, Options{})
		if len(res.Actions) != 1 || res.Actions[0].Kind != ActionCreateTarget {
			t.Errorf("actions = %v, want one create-target", res.Actions)
		}
		wantTargetCount("Pay Mortgage", 1)(t, f)
		f.mustSettle(t)
	})
}

func TestSyncConflictHubWins(t *testing.T) {
	f := newFixture(TwoWay)
	milk := f.hub.add("Milk", false)
	f.sync(t, Options{})

	f.hub.set(t, milk.ID, "Oat milk", false)
	f.target.set(t, f.target.items[0].ID, "Almond milk", false)
	res := f.sync(t, Options{})

	if len(res.Actions) != 1 || !res.Actions[0].Conflict {
		t.Fatalf("actions = %v, want one conflict", res.Actions)
	}
	if got := titles(f.target.items); !got["Oat milk"] {
		t.Errorf("target = %v, want Oat milk", f.target.items)
	}
	f.mustSettle(t)
}

func TestSyncRecoveryCleansUpCopyOfDeletedSource(t *testing.T) {
	f := newFixture(TwoWay)
	eggs := f.hub.add("Eggs", false)
	f.target.loseCreateResponse = true
	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); err == nil {
		t.Fatal("want an error")
	}
	f.target.loseCreateResponse = false
	f.clock.Advance(time.Minute)

	// The reminder is deleted before the next sync. Its orphaned copy must be
	// deleted too, not copied back into the hub as a new reminder.
	f.hub.remove(t, eggs.ID)
	f.sync(t, Options{})

	if len(f.hub.items) != 0 || len(f.target.items) != 0 {
		t.Errorf("hub %v, target %v; want both empty", f.hub.items, f.target.items)
	}
	f.mustSettle(t)
}

func TestSyncRecoveryWithTwoPossibleMatchesLinksNeither(t *testing.T) {
	f := newFixture(OneWay)
	f.hub.add("Eggs", false)
	f.target.loseCreateResponse = true
	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); err == nil {
		t.Fatal("want an error")
	}
	f.target.loseCreateResponse = false
	// Someone also added "Eggs" on the frame in the meantime.
	frameEggs := f.target.add("Eggs", false)
	f.clock.Advance(time.Minute)
	f.sync(t, Options{})

	for _, l := range f.links(t) {
		if l.TargetID == frameEggs.ID || l.TargetID == f.target.items[0].ID {
			t.Fatalf("linked %s, want neither ambiguous match linked", l.TargetID)
		}
	}
	if pending, _ := f.state.Pending(context.Background(), f.pair.ID); len(pending) != 0 {
		t.Errorf("pending = %v, want it dropped", pending)
	}
	f.mustSettle(t)
}
