//go:build unix

// Package lock keeps two remindersync processes, such as the daemon and a
// manual sync, from writing to the same lists and state at once.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("another remindersync is already syncing")

// Lock is an exclusive advisory lock on a file. The operating system releases
// it if the process dies, so a crash never leaves it stuck.
type Lock struct {
	f *os.File
}

// Acquire takes the lock at path without waiting, creating the file and its
// directory if needed. It returns [ErrHeld] if another process has it.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release gives the lock up.
func (l *Lock) Release() error {
	return l.f.Close()
}
