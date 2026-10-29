// Package notify shows notifications from ReminderSync.app. Notifications
// belong to an app bundle, so they only work in a process running the app's
// own binary: the daemon installed from the app, or a command run from it.
package notify

import "errors"

// ErrNoApp means the process isn't running from an app bundle, such as a
// plain `go run` build or a test.
var ErrNoApp = errors.New("notify: notifications need ReminderSync.app")

// Permission is the user's answer to whether the app may notify them.
type Permission int

// Permissions.
const (
	// Pending means the user hasn't answered yet.
	Pending Permission = iota
	Allowed
	Denied
)

func (p Permission) String() string {
	switch p {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	}
	return "pending"
}
