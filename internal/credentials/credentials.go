// Package credentials keeps target credentials in the macOS Keychain.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/zackwag/remindersync/skylight"
)

const (
	service         = "remindersync"
	skylightAccount = "skylight"
)

// ErrNotFound means no credentials are stored yet.
var ErrNotFound = errors.New("credentials not found; run `remindersync skylight login`")

// Skylight is everything needed to rebuild an authenticated Skylight client.
type Skylight struct {
	skylight.Token
	DeviceFingerprint string `json:"device_fingerprint"`
}

// LoadSkylight reads the stored Skylight credentials.
func LoadSkylight() (Skylight, error) {
	raw, err := keyring.Get(service, skylightAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return Skylight{}, ErrNotFound
	}
	if err != nil {
		return Skylight{}, fmt.Errorf("read Skylight credentials from Keychain: %w", err)
	}
	var creds Skylight
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return Skylight{}, fmt.Errorf("decode Skylight credentials: %w", err)
	}
	return creds, nil
}

// SaveSkylight stores Skylight credentials, replacing any existing ones.
func SaveSkylight(creds Skylight) error {
	raw, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("encode Skylight credentials: %w", err)
	}
	if err := keyring.Set(service, skylightAccount, string(raw)); err != nil {
		return fmt.Errorf("write Skylight credentials to Keychain: %w", err)
	}
	return nil
}
