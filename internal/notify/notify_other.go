//go:build !darwin || !cgo

package notify

import "time"

// Authorize always fails off macOS.
func Authorize(time.Duration) (Permission, error) {
	return Pending, ErrNoApp
}

// Send always fails off macOS.
func Send(string, string) error {
	return ErrNoApp
}
