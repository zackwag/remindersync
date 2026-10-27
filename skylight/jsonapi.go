package skylight

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Most Skylight GETs return JSON:API documents. Request bodies are a mix:
// list items, for one, take flat JSON rather than a JSON:API envelope.

type document struct {
	Data     json.RawMessage `json:"data"`
	Included []resource      `json:"included"`
}

type resource struct {
	ID         id              `json:"id"`
	Type       string          `json:"type"`
	Attributes json.RawMessage `json:"attributes"`
}

// id accepts both string and numeric JSON ids. Skylight returns strings today,
// but nothing guarantees it.
type id string

func (i *id) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*i = id(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("id is neither string nor number: %s", b)
	}
	*i = id(n.String())
	return nil
}

func decodeDocument(body []byte) (document, error) {
	var doc document
	if err := json.Unmarshal(body, &doc); err != nil {
		return document{}, fmt.Errorf("decode JSON:API document: %w", err)
	}
	return doc, nil
}

// primary returns the document's primary data, whether it was a single
// resource or an array.
func (d document) primary() ([]resource, error) {
	trimmed := bytes.TrimSpace(d.Data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var rs []resource
		if err := json.Unmarshal(trimmed, &rs); err != nil {
			return nil, fmt.Errorf("decode primary data: %w", err)
		}
		return rs, nil
	}
	var r resource
	if err := json.Unmarshal(trimmed, &r); err != nil {
		return nil, fmt.Errorf("decode primary data: %w", err)
	}
	return []resource{r}, nil
}
