package engine

import (
	"fmt"
	"strings"
)

// ActionKind is what an [Action] does.
type ActionKind string

// Action kinds.
const (
	// ActionUpdate writes field changes to either or both sides of a link and
	// records the new baselines. With no changes it only records them.
	ActionUpdate ActionKind = "update"
	// ActionAdopt links two existing items with the same title on a pair's
	// first sync.
	ActionAdopt ActionKind = "adopt"
	// ActionCreateTarget copies an unlinked hub item to the target.
	ActionCreateTarget ActionKind = "create-target"
	// ActionCreateHub copies an unlinked target item to the hub.
	ActionCreateHub ActionKind = "create-hub"
	// ActionRecreateTarget replaces a linked target item that was deleted,
	// on a one-way pair.
	ActionRecreateTarget ActionKind = "recreate-target"
	// ActionDeleteTarget deletes the target item of a link whose hub item is
	// gone.
	ActionDeleteTarget ActionKind = "delete-target"
	// ActionCompleteHub completes the hub item of a link whose target item
	// was deleted, on a two-way pair.
	ActionCompleteHub ActionKind = "complete-hub"
	// ActionUnlink drops a link without writing to either side.
	ActionUnlink ActionKind = "unlink"
)

// Action is one step of a sync plan.
type Action struct {
	Kind ActionKind
	// Link is the existing link, for actions on linked items.
	Link Link
	// Hub and Target are the current items, where they exist.
	Hub    Item
	Target Item
	// HubChange and TargetChange are the writes for ActionUpdate.
	HubChange    Change
	TargetChange Change
	// Conflict is set when both sides changed the same field to different
	// values and the hub's value won.
	Conflict bool
}

func (a Action) String() string {
	switch a.Kind {
	case ActionUpdate:
		var parts []string
		if !a.HubChange.IsZero() {
			parts = append(parts, "hub "+a.HubChange.String())
		}
		if !a.TargetChange.IsZero() {
			parts = append(parts, "target "+a.TargetChange.String())
		}
		if len(parts) == 0 {
			parts = append(parts, "record current values")
		}
		s := fmt.Sprintf("update %q: %s", a.Hub.Title, strings.Join(parts, ", "))
		if a.Conflict {
			s += " (conflict, hub wins)"
		}
		return s
	case ActionAdopt:
		return fmt.Sprintf("link existing %q on both sides", a.Hub.Title)
	case ActionCreateTarget:
		return fmt.Sprintf("create %q on target", a.Hub.Title)
	case ActionCreateHub:
		return fmt.Sprintf("create %q in hub", a.Target.Title)
	case ActionRecreateTarget:
		return fmt.Sprintf("re-create %q on target", a.Hub.Title)
	case ActionDeleteTarget:
		return fmt.Sprintf("delete %q from target", a.Target.Title)
	case ActionCompleteHub:
		return fmt.Sprintf("complete %q in hub (deleted on target)", a.Hub.Title)
	case ActionUnlink:
		return fmt.Sprintf("stop tracking %q", a.Link.Hub.Title)
	}
	return string(a.Kind)
}

// plan works out what a sync should do, without doing it. See the sync
// algorithm in docs/design.md.
func plan(dir Direction, hubItems, targetItems []Item, links []Link) []Action {
	hubByID := indexByID(hubItems)
	targetByID := indexByID(targetItems)
	linkedHub := make(map[string]bool, len(links))
	linkedTarget := make(map[string]bool, len(links))

	var actions []Action
	for _, l := range links {
		linkedHub[l.HubID] = true
		linkedTarget[l.TargetID] = true
		h, hubOK := hubByID[l.HubID]
		t, targetOK := targetByID[l.TargetID]
		switch {
		case hubOK && targetOK:
			if a, ok := planUpdate(dir, l, h, t); ok {
				actions = append(actions, a)
			}
		case targetOK:
			actions = append(actions, Action{Kind: ActionDeleteTarget, Link: l, Target: t})
		case hubOK && h.Completed:
			actions = append(actions, Action{Kind: ActionUnlink, Link: l, Hub: h})
		case hubOK && dir == TwoWay:
			actions = append(actions, Action{Kind: ActionCompleteHub, Link: l, Hub: h})
		case hubOK:
			actions = append(actions, Action{Kind: ActionRecreateTarget, Link: l, Hub: h})
		default:
			actions = append(actions, Action{Kind: ActionUnlink, Link: l})
		}
	}

	// Completed items are never copied, so a list's history stays put.
	var newHub, newTarget []Item
	for _, h := range hubItems {
		if !linkedHub[h.ID] && !h.Completed {
			newHub = append(newHub, h)
		}
	}
	for _, t := range targetItems {
		if !linkedTarget[t.ID] && !t.Completed {
			newTarget = append(newTarget, t)
		}
	}

	if len(links) == 0 {
		var adopted []Action
		adopted, newHub, newTarget = adopt(newHub, newTarget)
		actions = append(actions, adopted...)
	}
	for _, h := range newHub {
		actions = append(actions, Action{Kind: ActionCreateTarget, Hub: h})
	}
	if dir == TwoWay {
		for _, t := range newTarget {
			actions = append(actions, Action{Kind: ActionCreateHub, Target: t})
		}
	}
	return actions
}

// planUpdate merges a linked pair of items field by field. It returns false
// when neither side has changed since the last sync.
func planUpdate(dir Direction, l Link, h, t Item) (Action, bool) {
	if snapshot(h) == l.Hub && snapshot(t) == l.Target {
		return Action{}, false
	}
	a := Action{Kind: ActionUpdate, Link: l, Hub: h, Target: t}
	var titleConflict, completedConflict bool
	a.HubChange.Title, a.TargetChange.Title, titleConflict =
		mergeField(dir, h.Title, l.Hub.Title, t.Title, l.Target.Title)
	a.HubChange.Completed, a.TargetChange.Completed, completedConflict =
		mergeField(dir, h.Completed, l.Hub.Completed, t.Completed, l.Target.Completed)
	a.Conflict = titleConflict || completedConflict
	return a, true
}

// mergeField does a three-way merge of one field, comparing each side to its
// own baseline. It returns the value to write to each side, nil meaning no
// write, and whether both sides changed the field to different values.
func mergeField[T comparable](dir Direction, hub, hubBase, target, targetBase T) (toHub, toTarget *T, conflict bool) {
	hubChanged := hub != hubBase
	targetChanged := target != targetBase
	if (!hubChanged && !targetChanged) || hub == target {
		return nil, nil, false
	}
	switch {
	case hubChanged && targetChanged:
		return nil, &hub, true
	case targetChanged && dir == TwoWay:
		return &target, nil, false
	default:
		// The hub changed, or the target drifted on a one-way pair.
		return nil, &hub, false
	}
}

// adopt pairs up items whose title appears exactly once on each side. It's
// only used on a pair's first sync, so lists that already share items don't
// end up with two of each.
func adopt(hub, target []Item) (actions []Action, restHub, restTarget []Item) {
	hubCount := countTitles(hub)
	targetCount := countTitles(target)
	match := make(map[string]Item)
	for _, t := range target {
		if targetCount[t.Title] == 1 && hubCount[t.Title] == 1 {
			match[t.Title] = t
		}
	}

	adopted := make(map[string]bool)
	for _, h := range hub {
		t, ok := match[h.Title]
		if !ok {
			restHub = append(restHub, h)
			continue
		}
		actions = append(actions, Action{Kind: ActionAdopt, Hub: h, Target: t})
		adopted[t.ID] = true
	}
	for _, t := range target {
		if !adopted[t.ID] {
			restTarget = append(restTarget, t)
		}
	}
	return actions, restHub, restTarget
}

func indexByID(items []Item) map[string]Item {
	m := make(map[string]Item, len(items))
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}

func countTitles(items []Item) map[string]int {
	m := make(map[string]int, len(items))
	for _, it := range items {
		m[it.Title]++
	}
	return m
}
