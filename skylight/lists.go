package skylight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

// ErrItemNotReturned means Skylight accepted a create but its response didn't
// include the new list item, so the caller has to find it some other way.
var ErrItemNotReturned = errors.New("skylight: create response did not include the new list item")

// ItemStatus is a list item's completion state.
type ItemStatus string

const (
	StatusPending   ItemStatus = "pending"
	StatusCompleted ItemStatus = "completed"
)

// Frame is a Skylight household (one physical frame and its account data).
type Frame struct {
	ID string
	// Name is the household name the app displays, falling back to the
	// generated frame slug when there isn't one.
	Name string
}

// List is a to-do or grocery list on a frame.
type List struct {
	ID    string
	Label string
	Kind  string
	Color string
}

// ListItem is one entry on a [List].
type ListItem struct {
	ID       string
	Label    string
	Status   ItemStatus
	Position int

	// CreatedAt and UpdatedAt are passed through exactly as Skylight sends
	// them. Unverified: it isn't known yet whether list items carry these at
	// all, or in what format.
	CreatedAt string
	UpdatedAt string
}

// ListItemUpdate holds the fields to change on a list item. Nil fields are
// left as they are.
type ListItemUpdate struct {
	Label  *string     `json:"label,omitempty"`
	Status *ItemStatus `json:"status,omitempty"`
}

type frameAttributes struct {
	Name          string `json:"name"`
	HouseholdName string `json:"household_name"`
}

type listAttributes struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Color string `json:"color"`
}

type listItemAttributes struct {
	Label     string     `json:"label"`
	Status    ItemStatus `json:"status"`
	Position  *int       `json:"position"`
	CreatedAt *string    `json:"created_at"`
	UpdatedAt *string    `json:"updated_at"`
}

// Frames returns every frame on the account.
func (c *Client) Frames(ctx context.Context) ([]Frame, error) {
	doc, err := c.getDocument(ctx, "/api/frames")
	if err != nil {
		return nil, err
	}
	rs, err := doc.primary()
	if err != nil {
		return nil, err
	}
	frames := make([]Frame, 0, len(rs))
	for _, r := range rs {
		var a frameAttributes
		if err := decodeAttributes(r, &a); err != nil {
			return nil, err
		}
		name := a.HouseholdName
		if name == "" {
			name = a.Name
		}
		frames = append(frames, Frame{ID: string(r.ID), Name: name})
	}
	return frames, nil
}

// Lists returns the lists on a frame, without their items.
func (c *Client) Lists(ctx context.Context, frameID string) ([]List, error) {
	doc, err := c.getDocument(ctx, "/api/frames/"+url.PathEscape(frameID)+"/lists")
	if err != nil {
		return nil, err
	}
	rs, err := doc.primary()
	if err != nil {
		return nil, err
	}
	lists := make([]List, 0, len(rs))
	for _, r := range rs {
		var a listAttributes
		if err := decodeAttributes(r, &a); err != nil {
			return nil, err
		}
		lists = append(lists, List{ID: string(r.ID), Label: a.Label, Kind: a.Kind, Color: a.Color})
	}
	return lists, nil
}

// ListItems returns every item on a list, completed ones included, in the
// order the frame shows them.
func (c *Client) ListItems(ctx context.Context, frameID, listID string) ([]ListItem, error) {
	doc, err := c.getDocument(ctx, listPath(frameID, listID))
	if err != nil {
		return nil, err
	}
	var items []ListItem
	for _, r := range doc.Included {
		if r.Type != "list_item" {
			continue
		}
		item, err := toListItem(r)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	return items, nil
}

// AddListItem creates a pending item and returns it. If Skylight's response
// doesn't contain the new item, it returns [ErrItemNotReturned].
func (c *Client) AddListItem(ctx context.Context, frameID, listID, label string) (ListItem, error) {
	body, err := c.do(ctx, http.MethodPost, listPath(frameID, listID)+"/list_items", nil, map[string]string{"label": label})
	if err != nil {
		return ListItem{}, err
	}
	return findListItem(body, "")
}

// UpdateListItem changes an item's label and/or status and returns the
// updated item. Unverified: the HA integration sends "name" rather than
// "label" on update, while create definitely takes "label".
func (c *Client) UpdateListItem(ctx context.Context, frameID, listID, itemID string, update ListItemUpdate) (ListItem, error) {
	path := listPath(frameID, listID) + "/list_items/" + url.PathEscape(itemID)
	body, err := c.do(ctx, http.MethodPut, path, nil, update)
	if err != nil {
		return ListItem{}, err
	}
	return findListItem(body, itemID)
}

// DeleteListItem removes an item from a list.
func (c *Client) DeleteListItem(ctx context.Context, frameID, listID, itemID string) error {
	path := listPath(frameID, listID) + "/list_items/" + url.PathEscape(itemID)
	_, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	return err
}

func (c *Client) getDocument(ctx context.Context, path string) (document, error) {
	body, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return document{}, err
	}
	return decodeDocument(body)
}

// findListItem pulls a list_item out of a create or update response. The
// response shape isn't confirmed, so it accepts the item as the primary data
// (JSON:API or flat) or among included resources. wantID, when set, picks a
// specific item.
func findListItem(body []byte, wantID string) (ListItem, error) {
	if len(body) == 0 {
		return ListItem{}, ErrItemNotReturned
	}
	doc, err := decodeDocument(body)
	if err != nil {
		return ListItem{}, err
	}

	primary, err := doc.primary()
	if err != nil {
		return ListItem{}, err
	}
	candidates := append(primary, doc.Included...)
	for _, r := range candidates {
		if r.Type != "list_item" {
			continue
		}
		if wantID != "" && string(r.ID) != wantID {
			continue
		}
		return toListItem(r)
	}

	// Flat body: {"id": ..., "label": ..., "status": ...}.
	var flat struct {
		ID id `json:"id"`
		listItemAttributes
	}
	if err := json.Unmarshal(body, &flat); err == nil && flat.ID != "" && flat.Label != "" {
		return newListItem(string(flat.ID), flat.listItemAttributes), nil
	}
	return ListItem{}, ErrItemNotReturned
}

func toListItem(r resource) (ListItem, error) {
	var a listItemAttributes
	if err := decodeAttributes(r, &a); err != nil {
		return ListItem{}, err
	}
	return newListItem(string(r.ID), a), nil
}

func newListItem(itemID string, a listItemAttributes) ListItem {
	item := ListItem{ID: itemID, Label: a.Label, Status: a.Status}
	if item.Status == "" {
		item.Status = StatusPending
	}
	if a.Position != nil {
		item.Position = *a.Position
	}
	if a.CreatedAt != nil {
		item.CreatedAt = *a.CreatedAt
	}
	if a.UpdatedAt != nil {
		item.UpdatedAt = *a.UpdatedAt
	}
	return item
}

func decodeAttributes(r resource, v any) error {
	if len(r.Attributes) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Attributes, v); err != nil {
		return fmt.Errorf("decode %s %s attributes: %w", r.Type, r.ID, err)
	}
	return nil
}

func listPath(frameID, listID string) string {
	return "/api/frames/" + url.PathEscape(frameID) + "/lists/" + url.PathEscape(listID)
}
