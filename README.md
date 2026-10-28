# ReminderSync

Syncs Apple Reminders lists to other to-do targets, starting with [Skylight](https://www.skylightframe.com/) lists.

Apple Reminders is the hub: each sync pair links one Reminders list to one list on a target, and a Reminders list can feed several targets. The sync runs on a Mac, reading Reminders through EventKit and talking to each target's API directly.

## Status

Early. What's here so far:

- `skylight/`: a Go client for the Skylight API (OAuth login with PKCE, token refresh, frames, lists, and list items).
- `remindersync skylight ...`: CLI commands for logging in and working with Skylight lists directly.
- `remindersync reminders ...`: CLI commands for reading Apple Reminders lists through EventKit (macOS only).
- `remindersync config`: an interactive menu for adding and removing sync pairs, to a Skylight list or to someone's Skylight chores.
- `remindersync sync` and `remindersync status`: sync every pair once and see how each pair stands.
- `remindersync daemon`, `install` and `uninstall`: keep every pair in sync in the background, started at login by launchd.

## Set up sync pairs

```sh
go run ./cmd/remindersync config
```

Pick a Reminders list, a Skylight frame and list, and a direction (two-way, or one-way where Skylight mirrors Reminders), then name the pair. If Skylight isn't logged in yet, the wizard runs the login first. Pairs are saved to `~/Library/Application Support/remindersync/config.toml`. A Reminders list can be in several pairs, but a Skylight list can only be in one, since two pairs writing the same list would fight over it. Read-only Reminders lists can only be one-way.

## Chores

A pair can sync a Reminders list with one person's chores instead of a list. In `remindersync config`, pick "Someone's chores" and then the person. Each open reminder becomes a chore on its due date, and checking it off on either side checks off the other. A reminder with no due date goes on today and moves forward each day until it's done. A repeating reminder shows as one chore on its next date, which moves to the following date when it's checked off. Reminders due more than a year out wait until they're within a year.

## Sync

```sh
go run ./cmd/remindersync sync -dry-run
go run ./cmd/remindersync sync
go run ./cmd/remindersync status
```

Run `-dry-run` first, especially before a pair's first sync: it prints every change it would make and writes nothing. On a first sync, incomplete items whose titles appear exactly once on each side are linked rather than copied, and completed reminders are never copied. `-pair NAME` syncs one pair. If a sync would delete or complete more than `max(5, 25%)` of a pair's linked items, that pair is skipped until you rerun with `-force`. A pair that fails doesn't stop the others. Only one sync runs at a time; a dry run can run alongside it.

Sync state lives in `~/Library/Application Support/remindersync/state.db`. See [docs/design.md](docs/design.md) for how changes are detected and merged.

## Run in the background

```sh
go run ./cmd/remindersync install
go run ./cmd/remindersync status
```

`install` copies the binary to `~/Library/Application Support/remindersync/bin/remindersync`, writes a launchd agent to `~/Library/LaunchAgents/com.zackwag.remindersync.plist` and loads it, so `remindersync daemon` runs now and at every login. The daemon syncs when Reminders changes (after 2 s of quiet) and every minute to pick up changes on Skylight (`-poll` changes that). It logs changes and errors to `~/Library/Logs/remindersync/daemon.log`, and `status` shows whether it's running.

The first time, macOS asks whether `remindersync` can access your reminders, and `install` waits for you to click Allow. The permission is tied to that exact build, so after upgrading, run `install` again and allow the new build. Until you do, the daemon waits on the prompt and `status` says so.

`uninstall` stops the daemon and removes the agent and the installed binary. It keeps your pairs, state and log, so `sync` still works. A manual `sync` while the daemon runs is fine: they take turns.

## Reminders

Reminders access goes through EventKit by way of [go-eventkit](https://github.com/BRO3886/go-eventkit), so there's no iCloud login: ReminderSync sees whatever lists the Mac's Reminders app sees, including lists shared with you. The first run asks macOS for Reminders access, and the prompt names your terminal app rather than `remindersync`. If you deny it, turn it back on in System Settings > Privacy & Security > Reminders.

```sh
go run ./cmd/remindersync reminders lists
go run ./cmd/remindersync reminders items -list LIST_ID
go run ./cmd/remindersync reminders items -list LIST_ID -completed
```

## Skylight

Skylight doesn't publish its API. The client is based on what the Skylight apps send, mostly by way of the MIT-licensed [skylight_calendar](https://github.com/MegaTheLEGEND/skylight_calendar) Home Assistant integration, so it can break whenever Skylight changes things.

### Log in

```sh
go run ./cmd/remindersync skylight login
```

This prints a Skylight login URL. After you log in, the browser lands on a Skylight welcome page; paste that page's full URL back into the prompt. Tokens are stored in the macOS Keychain under the `remindersync` service and refreshed automatically.

### Work with lists

```sh
go run ./cmd/remindersync skylight frames
go run ./cmd/remindersync skylight lists -frame FRAME_ID
go run ./cmd/remindersync skylight items -frame FRAME_ID -list LIST_ID
go run ./cmd/remindersync skylight add -frame FRAME_ID -list LIST_ID "Buy milk"
go run ./cmd/remindersync skylight update -frame FRAME_ID -list LIST_ID -item ITEM_ID -status completed
go run ./cmd/remindersync skylight delete -frame FRAME_ID -list LIST_ID -item ITEM_ID
```

Add `-v` to any of these to log the full request and response bodies.

## Development

```sh
go vet ./...
go test -race ./...
```

CI also runs `golangci-lint` (see `.golangci.yml`), ShellCheck, and actionlint.

### End-to-end test

`internal/e2e` syncs a real Reminders list with a real Skylight list. It only builds with the `integration` tag and needs a Mac with Reminders access and a logged-in Skylight account, so CI doesn't run it. It creates a temporary Reminders list, briefly puts items on the Skylight list you point it at, and deletes both when it's done:

```sh
REMINDERSYNC_E2E_SKYLIGHT_FRAME=FRAME_ID REMINDERSYNC_E2E_SKYLIGHT_LIST=LIST_ID \
  go test -tags integration -v ./internal/e2e/
```

## License

[MIT](LICENSE)
