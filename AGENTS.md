# AGENTS.md

## Project overview

remindersync syncs Apple Reminders lists with Skylight (the family calendar frame): two-way with Skylight lists, and with one person's chores on the chore chart. Apple Reminders is the hub, read and written through EventKit with [go-eventkit](https://github.com/BRO3886/go-eventkit). Each target is reached through its own API.

It's a Go CLI (`remindersync`) that also runs as a launchd daemon. Releases ship it as `ReminderSync.app`, signed with a Developer ID and notarized, through the `zackwag/tap` Homebrew cask.

`docs/design.md` is the source of truth for behavior: the facts the design rests on (API quirks, TCC, EventKit), the sync algorithm, and the alternatives that were dismissed and why. Read the relevant section before changing behavior, and update it in the same PR.

## Setup

- Go 1.27 (see `go.mod`) and the Xcode command line tools. cgo is required on macOS: EventKit and UserNotifications are reached through Objective-C.
- A Mac with Reminders access and a Skylight login for anything beyond unit tests. Skylight tokens live in the Keychain (service `remindersync`), and `remindersync skylight login` creates them.
- User data lives in `~/Library/Application Support/remindersync/` (`config.toml` and `state.db`), and the daemon logs to `~/Library/Logs/remindersync/daemon.log`.

## Build / Run

```sh
go run ./cmd/remindersync <command>
scripts/build-app.sh                                  # ad hoc signed ReminderSync.app in dist/
scripts/build-app.sh -s "Developer ID Application"    # signed with the Developer ID, like releases
```

The Reminders permission (TCC) is checked against the binary's code signature. A plain `go build` is signed ad hoc, so its designated requirement is the build's hash, and every new build triggers the Reminders prompt again. To work on the daemon without re-approving each build, build with the Developer ID, move the app to `~/Applications`, and run `remindersync install` from it. The release workflow does the same build in CI.

Commands like `install`, a `sync` without `-dry-run`, and the daemon read and write the user's real Reminders and Skylight lists. Use `sync -dry-run` to preview, and don't write to real lists without asking.

## Test

```sh
go vet ./...
GOOS=linux go vet ./...
go test -race ./...
```

- CI (`.github/workflows/test.yml`) runs on Linux, so anything that only builds on macOS needs a stub: see `internal/mainloop` and `internal/notify` (`*_darwin.go` and `*_other.go`). `GOOS=linux go vet ./...` catches a missing one locally.
- Lint (`.github/workflows/lint.yml`) runs golangci-lint with `revive`, `errorlint`, `gocritic` and `misspell` (`.golangci.yml`), plus ShellCheck on `scripts/` and actionlint. golangci-lint isn't needed locally, but know that revive rejects unused parameters and variables that shadow builtins (`real`, `new`, `len` and so on).
- `internal/engine`, `internal/runner`, `internal/daemon`, `internal/launchd`, `internal/alert`, `internal/reminders` and `internal/target/skylight` are at 100% statement coverage. Keep them there: `go test -cover ./internal/...`.
- Engine tests run against the in-memory stores in `internal/engine/mem_test.go`, which can fail any call, lose a create's response, or behave like a repeating reminder. Sync bugs are reproduced there first.
- `internal/e2e` syncs a real Reminders list with a real Skylight list. It only builds with the `integration` tag and needs a Mac, so CI doesn't run it. See the README.

## Repository structure

- `cmd/remindersync/`: the CLI. One file per command group, plus `main.go`, which hands the main thread to the run loop and re-execs the app's binary when started through a symlink (like Homebrew's).
- `skylight/`: a client for the Skylight API (OAuth with PKCE, frames, lists, list items, chores, categories).
- `internal/engine/`: the sync engine. It only knows the `Store` and `State` interfaces.
- `internal/reminders/`: the hub store over one Reminders list.
- `internal/target/skylight/`: target stores for a Skylight list and for one person's chores.
- `internal/state/`: SQLite state (`modernc.org/sqlite`, pure Go), with numbered migrations.
- `internal/config/`, `internal/wizard/`: `config.toml` and the interactive `remindersync config` menu.
- `internal/runner/`: syncs every pair and prints `sync` and `status` output.
- `internal/daemon/`, `internal/launchd/`, `internal/lock/`, `internal/mainloop/`: the background daemon, its launchd agent, the sync lock, and the main-thread run loop that EventKit's change notifications need.
- `internal/alert/`, `internal/notify/`: failure alerts and native notifications from the app bundle.
- `internal/credentials/`: Keychain storage.
- `internal/e2e/`: the integration test.
- `packaging/`: `Info.plist`, entitlements and the app icon. `make-icon.swift` draws the icon, and `scripts/make-icon.sh` regenerates `AppIcon.icns`.
- `scripts/`: `build-app.sh` (universal binary, bundle, signing), `make-icon.sh`, and `configure-repo.sh` (GitHub repo settings).
- `docs/design.md`: the design.

## Conventions

- Match the surrounding code's comment density and style: doc comments on every exported identifier, and comments that explain why rather than what.
- Wrap errors with `%w` and check them with `errors.Is` and `errors.As` (errorlint enforces this). Sentinel errors are package-level `Err...` values.
- Prefer table-driven tests, and fakes over mocks.
- Skylight's API is undocumented, so check behavior against a live account with throwaway items before relying on it, and record what you find in the facts table in `docs/design.md`. For example, a chore `PUT` can't change `status` together with other fields, and a chore moved while completed can't be reopened.
- Markdown follows markdownlint with MD013 off: `-` bullets, `1.` for every ordered item, fenced code with a language, and one line per paragraph.

## Commit and PR conventions

- Commit messages and PR titles follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `ci:`, `chore:`, `refactor:`, `test:`), and a CI check enforces it. PRs are squash-merged, so the PR title becomes the commit on `main` and drives the release notes.
- Never commit to `main` directly. Branch, push and open a PR.
- Releases use release-please: don't hand-edit `CHANGELOG.md`, `.release-please-manifest.json` or version numbers, and don't touch `chore(main): release ...` PRs. Merging the release PR tags the release. The tag runs `.github/workflows/release.yml`, which builds, signs and notarizes `ReminderSync.app`, attaches it to the release, and updates the cask in `zackwag/homebrew-tap`. If a release fails, re-run it. If the workflow itself needed a fix, start it by hand with `workflow_dispatch` and the tag.
