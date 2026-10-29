package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// today is the fake clock's date in local time.
func (f *fixture) today() string {
	return f.clock.Now().Local().Format(DateLayout)
}

func (f *fixture) daysAhead(n int) string {
	return f.clock.Now().Local().AddDate(0, 0, n).Format(DateLayout)
}

func TestDatesSyncBothWays(t *testing.T) {
	f := newDateFixture(TwoWay)
	rent := f.hub.add("Pay rent", false)
	f.hub.setDue(t, rent.ID, "2026-11-01")
	f.sync(t, Options{})
	tRent := f.target.items[0]
	if tRent.Due != "2026-11-01" {
		t.Fatalf("target = %+v, want due 2026-11-01", tRent)
	}
	f.mustSettle(t)

	f.hub.setDue(t, rent.ID, "2026-11-03")
	f.sync(t, Options{})
	if got := f.target.items[0].Due; got != "2026-11-03" {
		t.Errorf("target due = %s after a hub move", got)
	}

	f.target.setDue(t, tRent.ID, "2026-11-05")
	f.sync(t, Options{})
	if got := f.hub.items[0].Due; got != "2026-11-05" {
		t.Errorf("hub due = %s after a target move", got)
	}
	f.mustSettle(t)
}

func TestDatesCopyToTheHub(t *testing.T) {
	f := newDateFixture(TwoWay)
	f.target.add("Clean the gutters", false)
	f.target.setDue(t, "t1", "2026-11-08")
	f.sync(t, Options{})
	if len(f.hub.items) != 1 || f.hub.items[0].Due != "2026-11-08" {
		t.Errorf("hub = %+v, want one item due 2026-11-08", f.hub.items)
	}
	f.mustSettle(t)
}

func TestUndatedHubItemFloatsOnToday(t *testing.T) {
	f := newDateFixture(TwoWay)
	f.hub.add("Get a haircut", false)
	f.sync(t, Options{})
	tCut := f.target.items[0]
	if tCut.Due != f.today() {
		t.Fatalf("target due = %s, want today (%s)", tCut.Due, f.today())
	}
	f.mustSettle(t)

	// Moving it on the target is undone; the hub never gets a date.
	f.target.setDue(t, tCut.ID, f.daysAhead(3))
	res := f.sync(t, Options{})
	want := `update "Get a haircut": target due=` + f.today()
	if len(res.Actions) != 1 || res.Actions[0].String() != want {
		t.Errorf("actions = %v, want [%s]", res.Actions, want)
	}
	f.mustSettle(t)

	// Each day it moves forward.
	f.clock.Advance(24 * time.Hour)
	f.sync(t, Options{})
	if got := f.target.items[0].Due; got != f.today() {
		t.Errorf("target due = %s the next day, want %s", got, f.today())
	}
	f.mustSettle(t)
	for _, w := range f.hub.writes {
		if strings.Contains(w, "due=") {
			t.Errorf("hub got a due date write: %s", w)
		}
	}

	// Once done, it stays put.
	f.target.set(t, tCut.ID, "Get a haircut", true)
	f.sync(t, Options{})
	if !f.hub.items[0].Completed || f.hub.items[0].Due != "" {
		t.Errorf("hub = %+v, want completed with no date", f.hub.items[0])
	}
	f.mustSettle(t)
	done := f.target.items[0].Due
	f.clock.Advance(48 * time.Hour)
	f.mustSettle(t)
	if got := f.target.items[0].Due; got != done {
		t.Errorf("a done item moved from %s to %s", done, got)
	}
}

func TestDatesHorizon(t *testing.T) {
	f := newDateFixture(TwoWay)
	alarm := f.hub.add("Replace smoke alarm", false)
	f.hub.setDue(t, alarm.ID, f.daysAhead(HorizonDays+30))
	f.sync(t, Options{})
	if len(f.target.items) != 0 {
		t.Fatalf("an item due past the horizon was copied: %+v", f.target.items)
	}

	f.hub.setDue(t, alarm.ID, f.daysAhead(HorizonDays))
	f.sync(t, Options{})
	if len(f.target.items) != 1 {
		t.Fatalf("an item due at the horizon wasn't copied: %+v", f.target.items)
	}

	// Moved back out, it leaves the target until it's in range again.
	f.hub.setDue(t, alarm.ID, f.daysAhead(HorizonDays+1))
	res := f.sync(t, Options{})
	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionDeleteTarget || len(f.target.items) != 0 {
		t.Errorf("actions = %v, target = %+v", res.Actions, f.target.items)
	}
	if len(f.hub.items) != 1 || f.hub.items[0].Completed {
		t.Errorf("the hub item changed: %+v", f.hub.items)
	}
	f.mustSettle(t)
}

func TestRepeatingHubItemMovesWithItsDate(t *testing.T) {
	f := newDateFixture(TwoWay)
	pay := f.hub.add("Pay Jennifer", false)
	f.hub.setDue(t, pay.ID, "2026-11-06")
	f.hub.repeating = map[string]bool{pay.ID: true}
	f.sync(t, Options{})
	tPay := f.target.items[0]

	f.target.set(t, tPay.ID, "Pay Jennifer", true)
	f.target.writes = nil
	f.sync(t, Options{})
	if h := f.hub.items[0]; h.Completed || h.Due != "2026-11-13" {
		t.Errorf("hub = %+v, want open and due 2026-11-13", h)
	}
	if tt := f.target.items[0]; tt.Completed || tt.Due != "2026-11-13" {
		t.Errorf("target = %+v, want reopened on 2026-11-13", tt)
	}
	want := []string{"update " + tPay.ID + " completed=false due=2026-11-13"}
	if !slices.Equal(f.target.writes, want) {
		t.Errorf("target writes = %v, want %v", f.target.writes, want)
	}
	f.mustSettle(t)
}

// TestRepeatingHubItemIsCompletedOnceWhenTheTargetFails checks that a target
// write failing after a repeating hub item was completed doesn't complete it
// again on every retry.
func TestRepeatingHubItemIsCompletedOnceWhenTheTargetFails(t *testing.T) {
	f := newDateFixture(TwoWay)
	pay := f.hub.add("Pay Jennifer", false)
	f.hub.setDue(t, pay.ID, "2026-11-06")
	f.hub.repeating = map[string]bool{pay.ID: true}
	f.sync(t, Options{})
	tPay := f.target.items[0]

	f.target.set(t, tPay.ID, "Pay Jennifer", true)
	f.target.setDue(t, tPay.ID, "2026-11-07")
	f.target.updateErr = errInjected
	for range 3 {
		if _, err := Sync(context.Background(), f.pair, f.state, Options{Now: f.clock.Now}); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected failure", err)
		}
	}
	if n := len(f.hub.items); n != 2 {
		t.Fatalf("hub has %d items, want the open one and one completed copy: %+v", n, f.hub.items)
	}

	f.heal()
	f.sync(t, Options{})
	if h := f.hub.items[0]; h.Completed || h.Due != "2026-11-14" {
		t.Errorf("hub = %+v, want open and due a week after the target's date", h)
	}
	if tt := f.target.items[0]; tt.Completed || tt.Due != "2026-11-14" {
		t.Errorf("target = %+v, want reopened on 2026-11-14", tt)
	}
	f.mustSettle(t)
}

func TestListPairIgnoresDates(t *testing.T) {
	f := newFixture(TwoWay)
	milk := f.hub.add("Milk", false)
	f.hub.setDue(t, milk.ID, "2026-11-01")
	f.sync(t, Options{})
	f.mustSettle(t)

	f.hub.setDue(t, milk.ID, "2026-11-02")
	f.mustSettle(t)
	if links := f.links(t); links[0].Hub.Due != "" || links[0].Target.Due != "" {
		t.Errorf("a list pair recorded dates: %+v", links[0])
	}
}
