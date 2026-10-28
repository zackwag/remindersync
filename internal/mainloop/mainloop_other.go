//go:build !darwin

// Package mainloop runs a command off the main thread on macOS, where
// EventKit needs the main run loop. Elsewhere it just runs it.
package mainloop

import "os"

// Run runs fn and exits the process with its result.
func Run(fn func() int) {
	os.Exit(fn())
}
