# ReminderSync

Syncs Apple Reminders lists to other to-do targets, starting with [Skylight](https://www.skylightframe.com/) lists.

Apple Reminders is the hub: each sync pair links one Reminders list to one list on a target, and a Reminders list can feed several targets. The sync runs on a Mac, reading Reminders through EventKit and talking to each target's API directly.

## Status

Early. What's here so far:

- `skylight/`: a Go client for the Skylight API (OAuth login with PKCE, token refresh, frames, lists, and list items).
- `remindersync skylight ...`: CLI commands for logging in and working with Skylight lists directly.
- `remindersync reminders ...`: CLI commands for reading Apple Reminders lists through EventKit (macOS only).

The sync engine isn't built yet.

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
