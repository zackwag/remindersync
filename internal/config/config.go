// Package config reads and writes config.toml, which holds the sync pairs.
// Credentials live in the Keychain and sync state in SQLite, never here.
package config

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/zackwag/remindersync/internal/engine"
)

// TargetSkylight is the only target type so far.
const TargetSkylight = "skylight"

// Config is the whole file.
type Config struct {
	Pairs []Pair `toml:"pair"`
}

// Pair links one Reminders list to one target list.
type Pair struct {
	// ID keys the pair's sync state, so renaming the pair doesn't orphan it.
	ID        string           `toml:"id"`
	Name      string           `toml:"name"`
	Direction engine.Direction `toml:"direction"`
	CreatedAt time.Time        `toml:"created_at"`
	Reminders RemindersList    `toml:"reminders"`
	Target    Target           `toml:"target"`
}

// RemindersList identifies the hub list. ListTitle is only for display and
// error messages; the list is always found by ID.
type RemindersList struct {
	ListID    string `toml:"list_id"`
	ListTitle string `toml:"list_title"`
}

// Target identifies the target list. Which fields apply depends on Type.
type Target struct {
	Type string `toml:"type"`

	// Skylight.
	FrameID string `toml:"frame_id,omitempty"`
	ListID  string `toml:"list_id,omitempty"`
	Label   string `toml:"label,omitempty"`
}

// String describes the pair on one line.
func (p Pair) String() string {
	arrow := "->"
	if p.Direction == engine.TwoWay {
		arrow = "<->"
	}
	return fmt.Sprintf("%s: Reminders %q %s %s", p.Name, p.Reminders.ListTitle, arrow, p.Target)
}

func (t Target) String() string {
	if t.Type == TargetSkylight {
		return fmt.Sprintf("Skylight %q", t.Label)
	}
	return t.Type
}

// DefaultPath is ~/Library/Application Support/remindersync/config.toml on
// macOS.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remindersync", "config.toml"), nil
}

// Load reads and validates the file at path. A missing file is an empty
// config, not an error.
func Load(path string) (Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("config: %s: unknown keys %v", path, undecoded)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// Save validates cfg and writes it to path, replacing the old file in one
// rename so a crash never leaves it half written.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("# Written by `remindersync config`. Edits are fine, but keep each pair's id.\n\n")
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: write: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// Validate checks every pair and the rules between pairs: IDs and names are
// unique, and no target list is used by more than one pair, since two pairs
// writing the same list would fight over it. Several pairs can share a
// Reminders list; that's how one list feeds several targets.
func (c Config) Validate() error {
	ids := make(map[string]bool)
	names := make(map[string]bool)
	targets := make(map[Target]string)
	for i, p := range c.Pairs {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("pair %d: %w", i+1, err)
		}
		if ids[p.ID] {
			return fmt.Errorf("pair %q: id %s is used twice", p.Name, p.ID)
		}
		if names[p.Name] {
			return fmt.Errorf("pair name %q is used twice", p.Name)
		}
		key := Target{Type: p.Target.Type, FrameID: p.Target.FrameID, ListID: p.Target.ListID}
		if other, ok := targets[key]; ok {
			return fmt.Errorf("pairs %q and %q both sync to %s", other, p.Name, p.Target)
		}
		ids[p.ID], names[p.Name], targets[key] = true, true, p.Name
	}
	return nil
}

// Validate checks one pair on its own.
func (p Pair) Validate() error {
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"id", p.ID},
		{"name", p.Name},
		{"reminders.list_id", p.Reminders.ListID},
		{"target.type", p.Target.Type},
	} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	if p.Direction != engine.OneWay && p.Direction != engine.TwoWay {
		return fmt.Errorf("pair %q: direction must be %q or %q, got %q", p.Name, engine.OneWay, engine.TwoWay, p.Direction)
	}
	switch p.Target.Type {
	case TargetSkylight:
		if p.Target.FrameID == "" || p.Target.ListID == "" {
			return fmt.Errorf("pair %q: skylight target needs frame_id and list_id", p.Name)
		}
	default:
		return fmt.Errorf("pair %q: unknown target type %q", p.Name, p.Target.Type)
	}
	return nil
}

// Find returns the pair with the given name.
func (c Config) Find(name string) (Pair, bool) {
	for _, p := range c.Pairs {
		if p.Name == name {
			return p, true
		}
	}
	return Pair{}, false
}

// NewID returns a random UUID (version 4) for a new pair.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
