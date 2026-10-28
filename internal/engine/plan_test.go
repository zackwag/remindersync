package engine

import (
	"reflect"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestPlanLinkedItems(t *testing.T) {
	base := Link{
		HubID:    "h1",
		TargetID: "t1",
		Hub:      Snapshot{Title: "Milk"},
		Target:   Snapshot{Title: "Milk"},
	}
	hub := func(title string, completed bool) []Item {
		return []Item{{ID: "h1", Title: title, Completed: completed}}
	}
	target := func(title string, completed bool) []Item {
		return []Item{{ID: "t1", Title: title, Completed: completed}}
	}

	tests := []struct {
		name         string
		dir          Direction
		hub, target  []Item
		kind         ActionKind // empty means no action
		hubChange    Change
		targetChange Change
		conflict     bool
	}{
		{
			name: "unchanged", dir: TwoWay,
			hub: hub("Milk", false), target: target("Milk", false),
		},
		{
			name: "hub title changed", dir: TwoWay,
			hub: hub("Oat milk", false), target: target("Milk", false),
			kind: ActionUpdate, targetChange: Change{Title: ptr("Oat milk")},
		},
		{
			name: "target completed, two-way", dir: TwoWay,
			hub: hub("Milk", false), target: target("Milk", true),
			kind: ActionUpdate, hubChange: Change{Completed: ptr(true)},
		},
		{
			name: "target completed, one-way reverts it", dir: OneWay,
			hub: hub("Milk", false), target: target("Milk", true),
			kind: ActionUpdate, targetChange: Change{Completed: ptr(false)},
		},
		{
			name: "both changed title differently, hub wins", dir: TwoWay,
			hub: hub("Oat milk", false), target: target("Almond milk", false),
			kind: ActionUpdate, targetChange: Change{Title: ptr("Oat milk")}, conflict: true,
		},
		{
			name: "both changed title the same way", dir: TwoWay,
			hub: hub("Oat milk", false), target: target("Oat milk", false),
			kind: ActionUpdate,
		},
		{
			name: "different fields changed on each side merge", dir: TwoWay,
			hub: hub("Oat milk", false), target: target("Milk", true),
			kind:         ActionUpdate,
			hubChange:    Change{Completed: ptr(true)},
			targetChange: Change{Title: ptr("Oat milk")},
		},
		{
			name: "hub gone deletes target", dir: TwoWay,
			target: target("Milk", false),
			kind:   ActionDeleteTarget,
		},
		{
			name: "target gone, hub completed, unlinks", dir: TwoWay,
			hub:  hub("Milk", true),
			kind: ActionUnlink,
		},
		{
			name: "target gone, hub open, two-way completes hub", dir: TwoWay,
			hub:  hub("Milk", false),
			kind: ActionCompleteHub,
		},
		{
			name: "target gone, hub open, one-way re-creates", dir: OneWay,
			hub:  hub("Milk", false),
			kind: ActionRecreateTarget,
		},
		{
			name: "both gone unlinks", dir: TwoWay,
			kind: ActionUnlink,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := plan(tt.dir, tt.hub, tt.target, []Link{base})
			if tt.kind == "" {
				if len(actions) != 0 {
					t.Fatalf("got %v, want no actions", actions)
				}
				return
			}
			if len(actions) != 1 {
				t.Fatalf("got %d actions %v, want 1", len(actions), actions)
			}
			a := actions[0]
			if a.Kind != tt.kind {
				t.Errorf("kind = %s, want %s", a.Kind, tt.kind)
			}
			if !reflect.DeepEqual(a.HubChange, tt.hubChange) {
				t.Errorf("hub change = %s, want %s", a.HubChange, tt.hubChange)
			}
			if !reflect.DeepEqual(a.TargetChange, tt.targetChange) {
				t.Errorf("target change = %s, want %s", a.TargetChange, tt.targetChange)
			}
			if a.Conflict != tt.conflict {
				t.Errorf("conflict = %t, want %t", a.Conflict, tt.conflict)
			}
		})
	}
}

func TestPlanUnlinkedItems(t *testing.T) {
	existing := Link{HubID: "h0", TargetID: "t0", Hub: Snapshot{Title: "Old"}, Target: Snapshot{Title: "Old"}}
	linkedHub := Item{ID: "h0", Title: "Old"}
	linkedTarget := Item{ID: "t0", Title: "Old"}

	tests := []struct {
		name        string
		dir         Direction
		hub, target []Item
		links       []Link
		want        []string
	}{
		{
			name:  "open hub item is created on target",
			dir:   OneWay,
			hub:   []Item{linkedHub, {ID: "h1", Title: "Eggs"}},
			links: []Link{existing}, target: []Item{linkedTarget},
			want: []string{`create "Eggs" on target`},
		},
		{
			name:  "completed hub item is never copied",
			dir:   TwoWay,
			hub:   []Item{linkedHub, {ID: "h1", Title: "Eggs", Completed: true}},
			links: []Link{existing}, target: []Item{linkedTarget},
		},
		{
			name:  "one-way leaves target-only items alone",
			dir:   OneWay,
			hub:   []Item{linkedHub},
			links: []Link{existing}, target: []Item{linkedTarget, {ID: "t1", Title: "Bread"}},
		},
		{
			name:  "two-way copies open target-only items to the hub",
			dir:   TwoWay,
			hub:   []Item{linkedHub},
			links: []Link{existing}, target: []Item{linkedTarget, {ID: "t1", Title: "Bread"}, {ID: "t2", Title: "Jam", Completed: true}},
			want: []string{`create "Bread" in hub`},
		},
		{
			name:   "first sync adopts titles that appear once on each side",
			dir:    TwoWay,
			hub:    []Item{{ID: "h1", Title: "Milk"}, {ID: "h2", Title: "Eggs"}},
			target: []Item{{ID: "t1", Title: "Milk"}, {ID: "t2", Title: "Bread"}},
			want: []string{
				`link existing "Milk" on both sides`,
				`create "Eggs" on target`,
				`create "Bread" in hub`,
			},
		},
		{
			name:   "first sync doesn't adopt ambiguous titles",
			dir:    OneWay,
			hub:    []Item{{ID: "h1", Title: "Milk"}, {ID: "h2", Title: "Milk"}},
			target: []Item{{ID: "t1", Title: "Milk"}},
			want: []string{
				`create "Milk" on target`,
				`create "Milk" on target`,
			},
		},
		{
			name:   "first sync doesn't adopt completed items",
			dir:    OneWay,
			hub:    []Item{{ID: "h1", Title: "Milk", Completed: true}},
			target: []Item{{ID: "t1", Title: "Milk"}},
		},
		{
			name:  "titles are only matched on the first sync",
			dir:   OneWay,
			hub:   []Item{linkedHub, {ID: "h1", Title: "Milk"}},
			links: []Link{existing}, target: []Item{linkedTarget, {ID: "t1", Title: "Milk"}},
			want: []string{`create "Milk" on target`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, a := range plan(tt.dir, tt.hub, tt.target, tt.links) {
				got = append(got, a.String())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
