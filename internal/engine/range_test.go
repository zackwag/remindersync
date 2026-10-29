package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestDaysAheadHoldsItemsBackUntilInRange(t *testing.T) {
	f := newDateFixture(TwoWay)
	f.pair.DaysAhead = 1
	rent := f.hub.add("Pay rent", false)
	f.hub.setDue(t, rent.ID, f.daysAhead(3))
	trash := f.hub.add("Take out trash", false)
	f.hub.setDue(t, trash.ID, f.daysAhead(1))
	f.hub.add("Call the vet", false)
	f.sync(t, Options{})

	if got := titleList(f.target.items); !slices.Equal(got, []string{"Take out trash", "Call the vet"}) {
		t.Fatalf("target = %v, want the item due tomorrow and the undated one", got)
	}
	f.mustSettle(t)

	f.clock.Advance(48 * time.Hour)
	f.sync(t, Options{})
	if got := f.target.byTitle("Pay rent"); len(got) != 1 || got[0].Due != f.daysAhead(1) {
		t.Errorf("target = %+v, want Pay rent once it's due tomorrow", f.target.items)
	}
}

func TestDaysAheadRepeatingItemLeavesTheTarget(t *testing.T) {
	f := newDateFixture(TwoWay)
	f.pair.DaysAhead = 1
	trash := f.hub.add("Take out trash", false)
	f.hub.setDue(t, trash.ID, f.today())
	f.hub.repeating = map[string]bool{trash.ID: true}
	f.sync(t, Options{})
	tTrash := f.target.items[0]

	f.target.set(t, tTrash.ID, "Take out trash", true)
	f.target.writes = nil
	f.sync(t, Options{})
	if len(f.hub.items) != 2 || f.hub.items[0].Completed || f.hub.items[0].Due != f.daysAhead(7) {
		t.Errorf("hub = %+v, want it completed once and moved a week on", f.hub.items)
	}
	if want := []string{"delete " + tTrash.ID}; !slices.Equal(f.target.writes, want) {
		t.Errorf("target writes = %v, want %v", f.target.writes, want)
	}
	if len(f.links(t)) != 0 {
		t.Errorf("links = %+v, want the link dropped", f.links(t))
	}
	f.mustSettle(t)

	f.clock.Advance(6 * 24 * time.Hour)
	f.sync(t, Options{})
	if len(f.target.items) != 1 || f.target.items[0].Due != f.daysAhead(1) || f.target.items[0].Completed {
		t.Errorf("target = %+v, want it back the day before it's due", f.target.items)
	}
}

func TestDaysAheadRemovesItemsMovedOutOnTheTarget(t *testing.T) {
	f := newDateFixture(TwoWay)
	f.pair.DaysAhead = 1
	trash := f.hub.add("Take out trash", false)
	f.hub.setDue(t, trash.ID, f.today())
	f.sync(t, Options{})

	// Moved out on the frame: the hub takes the date and the chore goes in
	// the same step.
	f.target.setDue(t, f.target.items[0].ID, f.daysAhead(10))
	f.sync(t, Options{})
	if f.hub.items[0].Due != f.daysAhead(10) || len(f.target.items) != 0 {
		t.Errorf("hub = %+v, target = %+v", f.hub.items, f.target.items)
	}
	f.mustSettle(t)

	// Added out of range on the frame: copied to the hub, then removed until
	// it's in range.
	vet := f.target.add("Call the vet", false)
	f.target.setDue(t, vet.ID, f.daysAhead(20))
	f.sync(t, Options{})
	if len(f.hub.byTitle("Call the vet")) != 1 {
		t.Fatalf("hub = %+v, want the chore copied", f.hub.items)
	}
	f.sync(t, Options{})
	if len(f.target.byTitle("Call the vet")) != 0 || f.hub.byTitle("Call the vet")[0].Completed {
		t.Errorf("hub = %+v, target = %+v", f.hub.items, f.target.items)
	}
	f.mustSettle(t)

	f.clock.Advance(19 * 24 * time.Hour)
	f.sync(t, Options{})
	if got := f.target.byTitle("Call the vet"); len(got) != 1 || got[0].Due != f.daysAhead(1) {
		t.Errorf("target = %+v, want the vet back the day before", f.target.items)
	}
}

func TestDaysAheadOnAListPair(t *testing.T) {
	f := newFixture(TwoWay)
	f.pair.DaysAhead = 7
	milk := f.hub.add("Milk", false)
	f.hub.setDue(t, milk.ID, f.daysAhead(10))
	f.hub.add("Eggs", false)
	f.sync(t, Options{})
	if got := titleList(f.target.items); !slices.Equal(got, []string{"Eggs"}) {
		t.Fatalf("target = %v, want only the undated item", got)
	}

	f.hub.setDue(t, milk.ID, f.daysAhead(3))
	f.sync(t, Options{})
	if len(f.target.byTitle("Milk")) != 1 {
		t.Fatalf("target = %+v, want Milk once it's in range", f.target.items)
	}

	// A list item has no date, so moving the reminder out always removes it.
	f.hub.setDue(t, milk.ID, f.daysAhead(10))
	f.sync(t, Options{})
	if len(f.target.byTitle("Milk")) != 0 || f.hub.items[0].Completed {
		t.Errorf("hub = %+v, target = %+v", f.hub.items, f.target.items)
	}
	f.mustSettle(t)
}

func TestDaysAheadFirstSyncAdoptsInsteadOfDuplicating(t *testing.T) {
	f := newFixture(TwoWay)
	f.pair.DaysAhead = 7
	milk := f.hub.add("Milk", false)
	f.hub.setDue(t, milk.ID, f.daysAhead(30))
	f.target.add("Milk", false)

	res := f.sync(t, Options{})
	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionAdopt {
		t.Fatalf("actions = %v, want Milk adopted", res.Actions)
	}
	// Once linked it's out of range like any other item.
	f.sync(t, Options{})
	if len(f.hub.items) != 1 || len(f.target.items) != 0 {
		t.Errorf("hub = %+v, target = %+v", f.hub.items, f.target.items)
	}
	f.mustSettle(t)
}

func TestDaysAheadFailedDeleteIsRetriedWithoutCompletingAgain(t *testing.T) {
	f := newDateFixture(TwoWay)
	f.pair.DaysAhead = 1
	trash := f.hub.add("Take out trash", false)
	f.hub.setDue(t, trash.ID, f.today())
	f.hub.repeating = map[string]bool{trash.ID: true}
	f.sync(t, Options{})

	f.target.set(t, f.target.items[0].ID, "Take out trash", true)
	f.target.deleteErr = errInjected
	if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected failure", err)
	}

	f.heal()
	f.sync(t, Options{})
	if len(f.hub.items) != 2 || len(f.target.items) != 0 {
		t.Errorf("hub = %+v, target = %+v, want one completion and the chore gone", f.hub.items, f.target.items)
	}
	f.mustSettle(t)
}

func titleList(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}
