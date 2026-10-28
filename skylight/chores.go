package skylight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// ErrChoreNotReturned means Skylight accepted a chore create or update but
// its response didn't include the chore.
var ErrChoreNotReturned = errors.New("skylight: response did not include the chore")

// ChoreStatus is a chore's completion state. Chores use "complete" where list
// items use "completed".
type ChoreStatus string

// Chore statuses Skylight uses.
const (
	ChorePending  ChoreStatus = "pending"
	ChoreComplete ChoreStatus = "complete"
)

// Category is a person or group that chores are assigned to.
type Category struct {
	ID    string
	Label string
	// ChoreChart is set when the category has a column on the chore chart.
	ChoreChart bool
}

// Chore is one chore. Repeating chores come back as one Chore per occurrence
// in the requested range.
type Chore struct {
	ID      string
	Summary string
	Status  ChoreStatus
	// Start is the date it's due, as YYYY-MM-DD.
	Start      string
	CategoryID string
	Recurring  bool
	UpForGrabs bool
	// CompletedOn is the date it was checked off, or "". A completed chore
	// is listed under this date, not Start.
	CompletedOn string
}

// ChoreQuery picks the chores to list.
type ChoreQuery struct {
	// After and Before bound the dates, inclusive, as YYYY-MM-DD. Pending
	// chores match on their start date and completed ones on the date they
	// were completed.
	After, Before string
	// CategoryID, if set, limits the list to one category.
	CategoryID string
	// IncludeLate also returns pending chores that started before After.
	IncludeLate bool
}

// NewChore is a one-off chore to create.
type NewChore struct {
	Summary    string `json:"summary"`
	Start      string `json:"start"`
	CategoryID string `json:"category_id"`
}

// ChoreUpdate holds the fields to change on a chore. Nil fields are left as
// they are.
type ChoreUpdate struct {
	Summary *string      `json:"summary,omitempty"`
	Start   *string      `json:"start,omitempty"`
	Status  *ChoreStatus `json:"status,omitempty"`
}

type categoryAttributes struct {
	Label      string `json:"label"`
	ChoreChart bool   `json:"selected_for_chore_chart"`
}

type choreAttributes struct {
	Summary     string      `json:"summary"`
	Status      ChoreStatus `json:"status"`
	Start       string      `json:"start"`
	Recurring   bool        `json:"recurring"`
	UpForGrabs  bool        `json:"up_for_grabs"`
	CompletedOn *string     `json:"completed_on"`
}

type choreResource struct {
	resource
	Relationships struct {
		Category struct {
			Data *struct {
				ID id `json:"id"`
			} `json:"data"`
		} `json:"category"`
	} `json:"relationships"`
}

// Categories returns the frame's chore categories.
func (c *Client) Categories(ctx context.Context, frameID string) ([]Category, error) {
	doc, err := c.getDocument(ctx, framePath(frameID)+"/categories")
	if err != nil {
		return nil, err
	}
	rs, err := doc.primary()
	if err != nil {
		return nil, err
	}
	cats := make([]Category, 0, len(rs))
	for _, r := range rs {
		var a categoryAttributes
		if err := decodeAttributes(r, &a); err != nil {
			return nil, err
		}
		cats = append(cats, Category{ID: string(r.ID), Label: a.Label, ChoreChart: a.ChoreChart})
	}
	return cats, nil
}

// Chores lists chores. Skylight returns at most 5,000 without saying it cut
// the list short, so callers should treat a full page as an error.
func (c *Client) Chores(ctx context.Context, frameID string, q ChoreQuery) ([]Chore, error) {
	query := url.Values{"after": {q.After}, "before": {q.Before}}
	if q.CategoryID != "" {
		query.Set("category_id", q.CategoryID)
	}
	if q.IncludeLate {
		query.Set("include_late", "true")
	}
	body, err := c.do(ctx, http.MethodGet, framePath(frameID)+"/chores", query, nil)
	if err != nil {
		return nil, err
	}
	rs, err := choreResources(body)
	if err != nil {
		return nil, err
	}
	chores := make([]Chore, 0, len(rs))
	for _, r := range rs {
		ch, err := toChore(r)
		if err != nil {
			return nil, err
		}
		chores = append(chores, ch)
	}
	return chores, nil
}

// AddChore creates a one-off pending chore and returns it.
func (c *Client) AddChore(ctx context.Context, frameID string, chore NewChore) (Chore, error) {
	body, err := c.do(ctx, http.MethodPost, framePath(frameID)+"/chores", nil, chore)
	if err != nil {
		return Chore{}, err
	}
	return singleChore(body)
}

// UpdateChore changes a chore and returns it. Setting Status to pending
// reopens a completed chore.
func (c *Client) UpdateChore(ctx context.Context, frameID, choreID string, update ChoreUpdate) (Chore, error) {
	body, err := c.do(ctx, http.MethodPut, framePath(frameID)+"/chores/"+url.PathEscape(choreID), nil, update)
	if err != nil {
		return Chore{}, err
	}
	return singleChore(body)
}

// DeleteChore deletes a chore. Skylight answers 404 for one that's already
// gone, which comes back as an [*APIError].
func (c *Client) DeleteChore(ctx context.Context, frameID, choreID string) error {
	_, err := c.do(ctx, http.MethodDelete, framePath(frameID)+"/chores/"+url.PathEscape(choreID), nil, nil)
	return err
}

func choreResources(body []byte) ([]choreResource, error) {
	doc, err := decodeDocument(body)
	if err != nil {
		return nil, err
	}
	// The generic resource type drops relationships, which hold the
	// category, so the primary data is decoded here instead.
	data := bytes.TrimSpace(doc.Data)
	switch {
	case len(data) == 0 || bytes.Equal(data, []byte("null")):
		return nil, nil
	case data[0] == '[':
		var rs []choreResource
		if err := json.Unmarshal(data, &rs); err != nil {
			return nil, fmt.Errorf("decode chores: %w", err)
		}
		return rs, nil
	default:
		var r choreResource
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("decode chore: %w", err)
		}
		return []choreResource{r}, nil
	}
}

func singleChore(body []byte) (Chore, error) {
	if len(body) == 0 {
		return Chore{}, ErrChoreNotReturned
	}
	rs, err := choreResources(body)
	if err != nil {
		return Chore{}, err
	}
	for _, r := range rs {
		if r.Type == "chore" {
			return toChore(r)
		}
	}
	return Chore{}, ErrChoreNotReturned
}

func toChore(r choreResource) (Chore, error) {
	var a choreAttributes
	if err := decodeAttributes(r.resource, &a); err != nil {
		return Chore{}, err
	}
	ch := Chore{
		ID:         string(r.ID),
		Summary:    a.Summary,
		Status:     a.Status,
		Start:      a.Start,
		Recurring:  a.Recurring,
		UpForGrabs: a.UpForGrabs,
	}
	if a.CompletedOn != nil {
		ch.CompletedOn = *a.CompletedOn
	}
	if d := r.Relationships.Category.Data; d != nil {
		ch.CategoryID = string(d.ID)
	}
	return ch, nil
}

func framePath(frameID string) string {
	return "/api/frames/" + url.PathEscape(frameID)
}
