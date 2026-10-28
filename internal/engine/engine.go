// Package engine syncs one list in the hub (Apple Reminders) with one list on
// a target. It only knows the [Store] and [State] interfaces, so it can be
// tested without EventKit or a network. The algorithm and its reasoning are in
// docs/design.md.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrTooManyChanges means a sync would delete or complete more linked items
// than the safety limit allows. Nothing was written.
var ErrTooManyChanges = errors.New("engine: too many destructive changes")

const (
	// minChangeLimit is the floor of the destructive-change limit, so small
	// lists aren't blocked from ordinary use.
	minChangeLimit = 5

	// recoverySkew allows for the target's clock running behind ours when
	// matching an interrupted create by its CreatedAt.
	recoverySkew = time.Minute

	// HorizonDays limits a pair with dates to hub items due within this many
	// days. Further out isn't useful on a chore chart, and it bounds how far
	// ahead a target has to read.
	HorizonDays = 365
)

// Pair is one sync pair: a hub list, a target list and a direction.
type Pair struct {
	// ID scopes the pair's state.
	ID        string
	Direction Direction
	Hub       Store
	Target    Store
	// Dates is set when the target keeps a due date on every item, like
	// Skylight chores. Due dates are then synced, hub items due beyond
	// [HorizonDays] are left out, and open hub items with no due date go on
	// today and move forward each day. Otherwise due dates are ignored.
	Dates bool
}

// Options adjusts a sync.
type Options struct {
	// DryRun plans the sync without writing to either side or to state.
	DryRun bool
	// Force skips the destructive-change limit.
	Force bool
	// Now defaults to time.Now.
	Now func() time.Time
	// Logger defaults to discarding everything.
	Logger *slog.Logger
}

// Result describes a sync.
type Result struct {
	// Actions is the full plan.
	Actions []Action
	// Applied is how many actions were carried out, in order.
	Applied int
}

// Sync reads both sides of a pair, plans the changes and applies them. Every
// action is saved to state as soon as it succeeds, so a failure part way
// leaves state consistent and the next sync picks up where this one stopped.
func Sync(ctx context.Context, p Pair, st State, opts Options) (Result, error) {
	s := syncer{pair: p, state: st, now: opts.Now, log: opts.Logger}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.log = s.log.With("pair", p.ID)

	hubItems, err := p.Hub.Items(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read hub: %w", err)
	}
	hubItems = s.hubItems(hubItems)
	targetItems, err := p.Target.Items(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read target: %w", err)
	}
	links, err := st.Links(ctx, p.ID)
	if err != nil {
		return Result{}, fmt.Errorf("read links: %w", err)
	}
	pending, err := st.Pending(ctx, p.ID)
	if err != nil {
		return Result{}, fmt.Errorf("read pending creates: %w", err)
	}

	recovered := s.recoverCreates(pending, links, hubItems, targetItems)
	for _, r := range recovered {
		if r.link != nil {
			links = append(links, *r.link)
		}
	}

	res := Result{Actions: plan(p.Direction, hubItems, targetItems, links)}
	if !opts.Force {
		if err := checkLimits(res.Actions, len(links)); err != nil {
			return res, err
		}
	}
	if opts.DryRun {
		return res, nil
	}

	for _, r := range recovered {
		if err := s.saveRecovery(ctx, r); err != nil {
			return res, err
		}
	}
	for _, a := range res.Actions {
		if err := s.apply(ctx, a); err != nil {
			return res, fmt.Errorf("%s: %w", a, err)
		}
		if a.Conflict {
			s.log.Warn("both sides changed; hub wins", "action", a.String())
		}
		res.Applied++
	}
	return res, nil
}

// checkLimits refuses a plan that deletes or completes more than
// max(minChangeLimit, 25%) of the pair's linked items on either side.
func checkLimits(actions []Action, links int) error {
	limit := max(minChangeLimit, links/4)
	var deletes, completes int
	for _, a := range actions {
		switch a.Kind {
		case ActionDeleteTarget:
			deletes++
		case ActionCompleteHub:
			completes++
		}
	}
	if deletes > limit || completes > limit {
		return fmt.Errorf("%w: would delete %d target items and complete %d hub items out of %d linked (limit %d each)",
			ErrTooManyChanges, deletes, completes, links, limit)
	}
	return nil
}

type syncer struct {
	pair  Pair
	state State
	now   func() time.Time
	log   *slog.Logger
}

// hubItems prepares the hub's items for planning: see [Pair.Dates].
func (s *syncer) hubItems(items []Item) []Item {
	out := make([]Item, 0, len(items))
	horizon := s.now().Local().AddDate(0, 0, HorizonDays).Format(DateLayout)
	for _, it := range items {
		it = s.hubView(it)
		if s.pair.Dates && it.Due > horizon {
			continue
		}
		out = append(out, it)
	}
	return out
}

// hubView is a hub item as the planner sees it: without a due date when the
// target doesn't keep dates, and on today when it does and the item is open
// with none.
func (s *syncer) hubView(it Item) Item {
	switch {
	case !s.pair.Dates:
		it.Due = ""
	case it.Due == "" && !it.Completed:
		it.Due = s.now().Local().Format(DateLayout)
		it.floating = true
	}
	return it
}

func (s *syncer) apply(ctx context.Context, a Action) error {
	switch a.Kind {
	case ActionUpdate:
		h, t := a.Hub, a.Target
		var err error
		targetChange := a.TargetChange
		if !a.HubChange.IsZero() {
			if h, err = s.pair.Hub.Update(ctx, h.ID, a.HubChange); err != nil {
				return err
			}
			h = s.hubView(h)
			if kept := overruled(a.HubChange, a.Hub, h); !kept.IsZero() {
				s.log.Info("hub kept its own value; writing it to the target", "item", h.Title, "hub", kept.String())
				targetChange = targetChange.with(kept)
			}
		}
		if !targetChange.IsZero() {
			if t, err = s.pair.Target.Update(ctx, t.ID, targetChange); err != nil {
				return err
			}
		}
		return s.state.PutLink(ctx, s.pair.ID, s.link(h, t))

	case ActionAdopt:
		return s.state.PutLink(ctx, s.pair.ID, s.link(a.Hub, a.Target))

	case ActionCreateTarget, ActionRecreateTarget:
		return s.create(ctx, SideTarget, a.Hub)

	case ActionCreateHub:
		return s.create(ctx, SideHub, a.Target)

	case ActionDeleteTarget:
		if err := s.pair.Target.Delete(ctx, a.Target.ID); err != nil {
			return err
		}
		return s.state.DeleteLink(ctx, s.pair.ID, a.Link.HubID)

	case ActionCompleteHub:
		done := true
		if _, err := s.pair.Hub.Update(ctx, a.Hub.ID, Change{Completed: &done}); err != nil {
			return err
		}
		return s.state.DeleteLink(ctx, s.pair.ID, a.Link.HubID)

	case ActionUnlink:
		return s.state.DeleteLink(ctx, s.pair.ID, a.Link.HubID)
	}
	return fmt.Errorf("unknown action kind %q", a.Kind)
}

// overruled returns the hub's values for the fields where an update didn't
// end up as asked: fields in ch that the hub didn't take, and fields not in
// ch that changed anyway. Completing a repeating reminder is the case that
// matters: EventKit leaves it open and moves its due date to the next
// occurrence, so the target item that was checked off has to be reopened and
// moved to match.
func overruled(ch Change, before, after Item) Change {
	var out Change
	out.Title = kept(ch.Title, before.Title, after.Title)
	out.Completed = kept(ch.Completed, before.Completed, after.Completed)
	out.Due = kept(ch.Due, before.Due, after.Due)
	return out
}

// kept returns after when a field didn't end up as asked, or nil.
func kept[T comparable](asked *T, before, after T) *T {
	if (asked != nil && *asked != after) || (asked == nil && after != before) {
		return &after
	}
	return nil
}

// create copies source to the given side, journaling the create first so an
// interrupted one can be recovered.
func (s *syncer) create(ctx context.Context, side Side, source Item) error {
	p := PendingCreate{Side: side, SourceID: source.ID, Title: source.Title, StartedAt: s.now()}
	if err := s.state.BeginCreate(ctx, s.pair.ID, p); err != nil {
		return err
	}
	store := s.pair.Target
	if side == SideHub {
		store = s.pair.Hub
	}
	created, err := store.Create(ctx, source)
	if err != nil {
		// The pending create stays, and the next sync's recovery decides
		// whether the item was made after all.
		return err
	}
	if side == SideHub {
		created = s.hubView(created)
	}
	return s.state.FinishCreate(ctx, s.pair.ID, side, source.ID, s.orient(side, source, created))
}

// link builds a link with both baselines taken from the items as they are now.
func (s *syncer) link(h, t Item) Link {
	return Link{HubID: h.ID, TargetID: t.ID, Hub: snapshot(h), Target: snapshot(t), SyncedAt: s.now()}
}

// orient builds the link for an item created on side as a copy of source.
func (s *syncer) orient(side Side, source, created Item) Link {
	if side == SideHub {
		return s.link(created, source)
	}
	return s.link(source, created)
}

// recovery is the outcome for one pending create: link it, or abandon it when
// link is nil.
type recovery struct {
	pending PendingCreate
	link    *Link
}

// recoverCreates decides what to do with creates a previous sync started but
// didn't finish. If exactly one unlinked item with the pending title was
// created on that side since the create started, it's the one we made, so it
// gets linked. That holds even if the source has since been deleted: the link
// then has a missing side, and the normal rules clean up the copy instead of
// copying it back. Otherwise the pending create is dropped and the normal
// rules create the item again if it's still needed. The worst case is one
// duplicate per interrupted create, never a loop.
func (s *syncer) recoverCreates(pending []PendingCreate, links []Link, hubItems, targetItems []Item) []recovery {
	if len(pending) == 0 {
		return nil
	}
	linkedHub := make(map[string]bool, len(links))
	linkedTarget := make(map[string]bool, len(links))
	for _, l := range links {
		linkedHub[l.HubID] = true
		linkedTarget[l.TargetID] = true
	}

	out := make([]recovery, 0, len(pending))
	for _, p := range pending {
		r := recovery{pending: p}
		candidates := createdSince(targetItems, linkedTarget, p)
		if p.Side == SideHub {
			candidates = createdSince(hubItems, linkedHub, p)
		}

		switch {
		case len(candidates) > 1:
			s.log.Warn("dropping interrupted create: more than one possible match", "side", p.Side, "title", p.Title, "matches", len(candidates))
		case len(candidates) == 1:
			// The source's baseline is what was copied, not its current
			// values, so an edit made since then still counts as a change.
			copied := Item{ID: p.SourceID, Title: p.Title}
			l := s.orient(p.Side, copied, candidates[0])
			r.link = &l
			linkedHub[l.HubID] = true
			linkedTarget[l.TargetID] = true
		}
		out = append(out, r)
	}
	return out
}

func (s *syncer) saveRecovery(ctx context.Context, r recovery) error {
	if r.link == nil {
		return s.state.AbandonCreate(ctx, s.pair.ID, r.pending.Side, r.pending.SourceID)
	}
	return s.state.FinishCreate(ctx, s.pair.ID, r.pending.Side, r.pending.SourceID, *r.link)
}

// createdSince returns the unlinked items titled p.Title that were created
// after p started, allowing for clock skew. Items with no CreatedAt never
// match, since there's no way to tell they're ours.
func createdSince(items []Item, linked map[string]bool, p PendingCreate) []Item {
	cutoff := p.StartedAt.Add(-recoverySkew)
	var out []Item
	for _, it := range items {
		if linked[it.ID] || it.Title != p.Title || it.CreatedAt.IsZero() || it.CreatedAt.Before(cutoff) {
			continue
		}
		out = append(out, it)
	}
	return out
}
