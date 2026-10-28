package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DateLayout is the format of [Item.Due].
const DateLayout = "2006-01-02"

// Item is one entry in a list, reduced to the fields the stores support.
type Item struct {
	ID        string
	Title     string
	Completed bool
	// Due is the due date as YYYY-MM-DD in local time, or "" for none. It's
	// only synced on pairs whose target keeps dates (see [Pair.Dates]).
	Due string

	// CreatedAt is when the store created the item. It's only used to recover
	// from a crash in the middle of a create, and is zero if the store doesn't
	// know.
	CreatedAt time.Time

	// floating marks a hub item with no due date that was given today's
	// date for a target that needs one.
	floating bool
}

// Change holds the fields to write to an item. Nil fields are left alone.
type Change struct {
	Title     *string
	Completed *bool
	Due       *string
}

// with returns c with the fields set in o replacing its own.
func (c Change) with(o Change) Change {
	if o.Title != nil {
		c.Title = o.Title
	}
	if o.Completed != nil {
		c.Completed = o.Completed
	}
	if o.Due != nil {
		c.Due = o.Due
	}
	return c
}

// IsZero reports whether the change writes nothing.
func (c Change) IsZero() bool {
	return c.Title == nil && c.Completed == nil && c.Due == nil
}

func (c Change) String() string {
	var parts []string
	if c.Title != nil {
		parts = append(parts, fmt.Sprintf("title=%q", *c.Title))
	}
	if c.Completed != nil {
		parts = append(parts, fmt.Sprintf("completed=%t", *c.Completed))
	}
	if c.Due != nil {
		parts = append(parts, "due="+*c.Due)
	}
	return strings.Join(parts, " ")
}

// Store is one side of a pair: a single list in the hub or on a target.
type Store interface {
	// Items returns every item in the list, completed ones included. It
	// returns the complete list or an error, never a partial list.
	Items(ctx context.Context) ([]Item, error)

	// Create adds an incomplete item with from's title, and its due date if
	// the store keeps dates, and returns it as stored.
	Create(ctx context.Context, from Item) (Item, error)

	// Update writes the non-nil fields of change and returns the item as
	// stored.
	Update(ctx context.Context, id string, change Change) (Item, error)

	// Delete removes an item. Deleting an item that's already gone is not an
	// error.
	Delete(ctx context.Context, id string) error
}

// Direction is which way a pair syncs.
type Direction string

// Pair directions.
const (
	// OneWay makes the target mirror the hub. Items added on the target are
	// left alone.
	OneWay Direction = "one-way"
	// TwoWay syncs changes in both directions, with the hub winning conflicts.
	TwoWay Direction = "two-way"
)

// Snapshot is the synced fields of an item as of the last sync.
type Snapshot struct {
	Title     string
	Completed bool
	Due       string
}

func snapshot(it Item) Snapshot {
	return Snapshot{Title: it.Title, Completed: it.Completed, Due: it.Due}
}

// Link ties a hub item to a target item. Hub and Target are each side's
// baseline: what that side returned at the last sync. A side has changed when
// its current values differ from its own baseline.
type Link struct {
	HubID    string
	TargetID string
	Hub      Snapshot
	Target   Snapshot
	SyncedAt time.Time
}

// Side names the side of a pair an item lives on.
type Side string

// Pair sides.
const (
	SideHub    Side = "hub"
	SideTarget Side = "target"
)

// PendingCreate records a create that has been started but not yet linked, so
// a crash between the store's response and the link being saved can be
// recovered without creating the item twice.
type PendingCreate struct {
	// Side is where the item is being created.
	Side Side
	// SourceID is the item being copied, on the other side.
	SourceID  string
	Title     string
	StartedAt time.Time
}

// State persists links and pending creates, scoped by pair ID.
type State interface {
	// Links returns a pair's links, ordered by HubID.
	Links(ctx context.Context, pairID string) ([]Link, error)

	// PutLink inserts or replaces the link for l.HubID.
	PutLink(ctx context.Context, pairID string, l Link) error

	// DeleteLink removes the link for hubID, if there is one.
	DeleteLink(ctx context.Context, pairID, hubID string) error

	// Pending returns a pair's unfinished creates.
	Pending(ctx context.Context, pairID string) ([]PendingCreate, error)

	// BeginCreate records p before the create is sent.
	BeginCreate(ctx context.Context, pairID string, p PendingCreate) error

	// FinishCreate removes the pending create for side and sourceID and puts
	// l, atomically.
	FinishCreate(ctx context.Context, pairID string, side Side, sourceID string, l Link) error

	// AbandonCreate removes the pending create for side and sourceID without
	// linking anything.
	AbandonCreate(ctx context.Context, pairID string, side Side, sourceID string) error
}
