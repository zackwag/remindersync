// Package e2e holds end-to-end tests that sync a real Apple Reminders list with
// a real Skylight list. They only build with the integration tag, need a Mac
// with Reminders access and a logged-in Skylight account, and briefly put
// items on the Skylight list named by REMINDERSYNC_E2E_SKYLIGHT_LIST:
//
//	REMINDERSYNC_E2E_SKYLIGHT_FRAME=5760005 REMINDERSYNC_E2E_SKYLIGHT_LIST=7763027 \
//		go test -tags integration -v ./internal/e2e/
package e2e
