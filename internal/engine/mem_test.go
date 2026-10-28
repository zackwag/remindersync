package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"
)

// fakeClock is a settable clock shared by the engine and the stores, so
// CreatedAt and pending-create times line up.
type fakeClock struct{ t time.Time }

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

var errLostResponse = errors.New("connection reset before the response arrived")

// memStore is an in-memory [Store].
type memStore struct {
	prefix string
	clock  *fakeClock
	items  []Item
	next   int

	// normalize, if set, rewrites titles on write, like a target that trims
	// whitespace.
	normalize func(string) string
	// repeating holds the IDs of items that behave like repeating reminders:
	// completing one leaves it open and adds a completed copy, as EventKit
	// does.
	repeating map[string]bool
	// itemsErr fails reads.
	itemsErr error
	// createErr fails creates before anything is made.
	createErr error
	// loseCreateResponse makes creates succeed but return an error, like a
	// crash between the API call and saving the link.
	loseCreateResponse bool
	// updateErr and deleteErr fail those writes before anything changes.
	updateErr error
	deleteErr error

	writes []string
}

func newMemStore(prefix string, clock *fakeClock) *memStore {
	return &memStore{prefix: prefix, clock: clock}
}

// add puts an item in the store directly, as if someone added it by hand.
func (m *memStore) add(title string, completed bool) Item {
	m.next++
	it := Item{
		ID:        fmt.Sprintf("%s%d", m.prefix, m.next),
		Title:     title,
		Completed: completed,
		CreatedAt: m.clock.Now(),
	}
	m.items = append(m.items, it)
	return it
}

func (m *memStore) Items(context.Context) ([]Item, error) {
	if m.itemsErr != nil {
		return nil, m.itemsErr
	}
	return slices.Clone(m.items), nil
}

func (m *memStore) Create(_ context.Context, title string) (Item, error) {
	if m.createErr != nil {
		return Item{}, m.createErr
	}
	m.writes = append(m.writes, "create "+title)
	it := m.add(m.norm(title), false)
	if m.loseCreateResponse {
		return Item{}, errLostResponse
	}
	return it, nil
}

func (m *memStore) Update(_ context.Context, id string, ch Change) (Item, error) {
	if m.updateErr != nil {
		return Item{}, m.updateErr
	}
	i := m.index(id)
	if i < 0 {
		return Item{}, fmt.Errorf("update %s: not found", id)
	}
	m.writes = append(m.writes, fmt.Sprintf("update %s %s", id, ch))
	if ch.Title != nil {
		m.items[i].Title = m.norm(*ch.Title)
	}
	if ch.Completed != nil {
		if *ch.Completed && m.repeating[id] {
			m.add(m.items[i].Title, true)
		} else {
			m.items[i].Completed = *ch.Completed
		}
	}
	return m.items[i], nil
}

func (m *memStore) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.writes = append(m.writes, "delete "+id)
	if i := m.index(id); i >= 0 {
		m.items = slices.Delete(m.items, i, i+1)
	}
	return nil
}

// set changes an item directly, as if someone edited it by hand.
func (m *memStore) set(t *testing.T, id string, title string, completed bool) {
	t.Helper()
	i := m.index(id)
	if i < 0 {
		t.Fatalf("set %s: not found", id)
	}
	m.items[i].Title, m.items[i].Completed = title, completed
}

// remove deletes an item directly, as if someone deleted it by hand.
func (m *memStore) remove(t *testing.T, id string) {
	t.Helper()
	i := m.index(id)
	if i < 0 {
		t.Fatalf("remove %s: not found", id)
	}
	m.items = slices.Delete(m.items, i, i+1)
}

func (m *memStore) byTitle(title string) []Item {
	var out []Item
	for _, it := range m.items {
		if it.Title == title {
			out = append(out, it)
		}
	}
	return out
}

func (m *memStore) index(id string) int {
	return slices.IndexFunc(m.items, func(it Item) bool { return it.ID == id })
}

func (m *memStore) norm(s string) string {
	if m.normalize == nil {
		return s
	}
	return m.normalize(s)
}

type pendingKey struct {
	pair     string
	side     Side
	sourceID string
}

// memState is an in-memory [State] that enforces the same uniqueness rules as
// the SQLite schema.
type memState struct {
	links   map[string]map[string]Link
	pending map[pendingKey]PendingCreate

	// fail makes the named method return the error without doing anything.
	fail map[string]error
}

func newMemState() *memState {
	return &memState{
		links:   make(map[string]map[string]Link),
		pending: make(map[pendingKey]PendingCreate),
		fail:    make(map[string]error),
	}
}

func (s *memState) Links(_ context.Context, pairID string) ([]Link, error) {
	if err := s.fail["Links"]; err != nil {
		return nil, err
	}
	var out []Link
	for _, l := range s.links[pairID] {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HubID < out[j].HubID })
	return out, nil
}

func (s *memState) PutLink(_ context.Context, pairID string, l Link) error {
	if err := s.fail["PutLink"]; err != nil {
		return err
	}
	for hubID, other := range s.links[pairID] {
		if hubID != l.HubID && other.TargetID == l.TargetID {
			return fmt.Errorf("target %s is already linked to hub %s", l.TargetID, hubID)
		}
	}
	if s.links[pairID] == nil {
		s.links[pairID] = make(map[string]Link)
	}
	s.links[pairID][l.HubID] = l
	return nil
}

func (s *memState) DeleteLink(_ context.Context, pairID, hubID string) error {
	if err := s.fail["DeleteLink"]; err != nil {
		return err
	}
	delete(s.links[pairID], hubID)
	return nil
}

func (s *memState) Pending(_ context.Context, pairID string) ([]PendingCreate, error) {
	if err := s.fail["Pending"]; err != nil {
		return nil, err
	}
	var out []PendingCreate
	for k, p := range s.pending {
		if k.pair == pairID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, nil
}

func (s *memState) BeginCreate(_ context.Context, pairID string, p PendingCreate) error {
	if err := s.fail["BeginCreate"]; err != nil {
		return err
	}
	s.pending[pendingKey{pairID, p.Side, p.SourceID}] = p
	return nil
}

func (s *memState) FinishCreate(ctx context.Context, pairID string, side Side, sourceID string, l Link) error {
	if err := s.fail["FinishCreate"]; err != nil {
		return err
	}
	if err := s.PutLink(ctx, pairID, l); err != nil {
		return err
	}
	delete(s.pending, pendingKey{pairID, side, sourceID})
	return nil
}

func (s *memState) AbandonCreate(_ context.Context, pairID string, side Side, sourceID string) error {
	if err := s.fail["AbandonCreate"]; err != nil {
		return err
	}
	delete(s.pending, pendingKey{pairID, side, sourceID})
	return nil
}
