# Sync design

How ReminderSync keeps an Apple Reminders list and a target list (starting with Skylight) in step, and why it works the way it does.

## Goals

- Pair one Reminders list with one target list, or with one person's Skylight chores, set up through an interactive wizard like `rclone config`.
- Sync title and completion both ways, plus due dates for chores, or one way from Reminders when the target should just mirror it.
- Let one Reminders list feed several targets.
- Never lose data or create runaway duplicates. The setup this replaces (the iPhone Companion app's Reminders Sync into Home Assistant) produced 762 copies of one Skylight chore, so this is the requirement everything else gives way to.

## Non-goals

- Syncing fields a target can't hold. Skylight list items only have a label and a status, and chores add a date, so notes, priority, URLs, tags and alarms stay in Reminders and are never touched, and due dates only sync with chores.
- Target-to-target sync. Targets only talk to the hub.
- Running anywhere but a Mac signed into the iCloud account. The tool uses EventKit, not iCloud's web APIs.

## Facts this is built on

From the spikes in #1 and #5, against real data:

| Fact | Consequence |
| --- | --- |
| Skylight `POST list_items` returns the new item with its ID. | Identity is always by ID. Titles are never used to find an item, except once during the first sync (see [First sync](#first-sync)). |
| Skylight items have `created_at` but no `updated_at`. | Changes on Skylight can only be found by comparing against the last values we saw. Last-writer-wins by timestamp isn't possible. |
| Skylight `PUT` accepts `label` and `status` independently. | Updates send only the field that changed. |
| Reminders have `ModifiedAt` and `CompletionDate`, and go-eventkit's `UpdateReminderInput` uses pointer fields. | Updates from a target set only `Title` and `Completed`, so fields Skylight doesn't have are never cleared. |
| Reading all 876 of my reminders takes about 130 ms. | Reading the whole hub every sync is fine. No incremental reads needed. |
| go-eventkit's `WithListID` is broken in v0.16.0. | Reminders are filtered by `ListID` in our code. |
| `Groceries` has 489 reminders, all completed. | Completed history must not be copied to a target. |
| A plain binary started by launchd gets its own Reminders permission prompt, under the binary's name. Until someone answers, `eventkit.New()` blocks. A run from Terminal is granted through Terminal, so it can't approve the daemon. The grant is checked against the program's designated requirement, which for the Go linker's ad hoc signature is the build's hash (`cdhash H"…"`), so every new build asks again. Signed with a Developer ID, it's `identifier "dev.zackwag.ReminderSync" and anchor apple generic and … certificate leaf[subject.OU] = "2Q9ERRLN2D"`, which stays the same across builds and certificate renewals. | Releases ship `ReminderSync.app` signed with the Developer ID, so you allow it once. `install` loads the agent and waits while you answer the prompt. The daemon records that it's waiting before it asks, so `status` can say why nothing is syncing. |
| Skylight chores: `POST /chores` with `summary`, `start` and `category_id` creates a one-off chore and returns it. `PUT /chores/{id}` changes `summary`, `start` and `status` (`complete` or `pending`, which reopens it), but returns 400 if one request changes `status` and either of the others. A completed chore's `summary` and `start` can still be changed, but if it's moved while completed and then reopened, the response says `pending` while every later `GET` still says `complete`. Reopening first and then moving works. `DELETE` returns 404 once it's gone. `GET /chores?after=&before=` lists them frame-wide, `category_id` filters to one person, and `include_late` adds pending chores from before `after`. | A chore store covers one category and reads one window per sync. A change to the status and other fields is sent as two requests, with the other fields changed while the chore is open: a reopen goes first and a completion last. |
| A completed chore is listed under the day it was completed, not its start date. | The read window starts 30 days back, so a chore checked off on the frame stays visible to the sync for a month. |
| Chores have no `created_at`. | Crash recovery can't match an interrupted chore create, so a crash at that moment can leave one duplicate chore. It can't loop. |
| go-eventkit always writes a time of day with a due date. | A date written to Reminders keeps the reminder's existing time, and a reminder that had only a date gets midnight. |
| Completing a repeating reminder through go-eventkit works like Reminders.app: the reminder keeps its ID, stays open and moves to its next due date, and a completed copy with a new ID is added. `UpdateReminder` returns the open reminder. | Completing a hub item can leave it open. The target item has to be reopened to match (see [Repeating reminders](#repeating-reminders)). |

## Architecture

Hub and spoke. Reminders is the hub, and each sync pair links one Reminders list to one target list. Two pairs can share a Reminders list. A change made on one target reaches the hub on that pair's run, and the other targets on their next run, so all sides agree after at most two passes.

```text
Skylight "Grocery List" <--pair "groceries"--> Reminders "Groceries" <--pair "groceries-x"--> (future target)
```

The engine works through one interface that both the hub and each target implement:

```go
type Item struct {
    ID        string
    Title     string
    Completed bool
    Due       string // YYYY-MM-DD, or "" for none
}

type Store interface {
    // Items returns every item in the list, completed ones included. It
    // either returns the complete list or an error, never a partial list.
    Items(ctx context.Context) ([]Item, error)
    Create(ctx context.Context, from Item) (Item, error)
    // Update changes only the non-nil fields and returns the item as stored.
    Update(ctx context.Context, id string, change Change) (Item, error)
    Delete(ctx context.Context, id string) error
}
```

Title and completion sync on every pair. Due dates sync only on pairs whose target keeps a date on every item, which today means Skylight chores (`Pair.Dates`). On other pairs the hub's dates are dropped before planning, so they're never compared or written.

Packages:

- `skylight`: the API client (exists).
- `internal/reminders`: a `Store` over go-eventkit for one list ID.
- `internal/target/skylight`: `Store` over one list on a frame, and `ChoreStore` over one category's chores.
- `internal/config`: reading and writing pairs.
- `internal/state`: the SQLite state store.
- `internal/engine`: the sync algorithm. It knows nothing about EventKit or Skylight, so it can be tested against in-memory stores.

## Configuration

`remindersync config` walks through:

1. Pick a Reminders list from `Lists()`, showing source and sharing.
1. Pick a target type (Skylight only for now). If it isn't logged in yet, run its login inline.
1. Pick the target's container and list (Skylight frame, then list).
1. Pick the direction: one-way (Reminders to target) or two-way.
1. Name the pair.

Pairs are saved to `config.toml` in `os.UserConfigDir()/remindersync` (`~/Library/Application Support/remindersync` on macOS):

```toml
[[pair]]
id = "6f1d2c1e-6c2c-4f7e-9b1a-0f0d7d0b9a11"
name = "groceries"
direction = "two-way"
created_at = 2026-10-28T14:00:00Z

[pair.reminders]
list_id = "B6A189C2-2C3A-4E96-A51E-8FB556D65CA9"
list_title = "Groceries"

[pair.target]
type = "skylight"
frame_id = "5760005"
list_id = "7763026"
label = "Grocery List"
# days_ahead = 7
```

The `id` is what the state store is keyed on, so renaming a pair doesn't orphan its state. Titles are stored only for display and error messages. Credentials stay in the Keychain, as they do now.

If a pair's Reminders list or target list can't be found by ID, the pair is skipped with an error naming the stored title, and nothing is written to either side. The fix is to re-pick the list in `remindersync config`. That can happen after an iCloud reset, which can change EventKit list IDs. There's deliberately no fallback to matching by title.

## State

A SQLite database in `os.UserConfigDir()/remindersync/state.db` (`~/Library/Application Support/remindersync/state.db` on macOS) holds one row per linked item. It's deliberately not in a cache directory: macOS can purge `~/Library/Caches`, and losing state turns every pair's next sync into a first sync, where deletes made in the meantime are missed and repeated titles get copied again.

The schema:

```sql
CREATE TABLE link (
    pair_id          TEXT NOT NULL,
    hub_id           TEXT NOT NULL,
    target_id        TEXT NOT NULL,
    hub_title        TEXT NOT NULL,
    hub_completed    INTEGER NOT NULL,
    target_title     TEXT NOT NULL,
    target_completed INTEGER NOT NULL,
    synced_at        TEXT NOT NULL,
    PRIMARY KEY (pair_id, hub_id),
    UNIQUE (pair_id, target_id)
);

CREATE TABLE pending_create (
    pair_id    TEXT NOT NULL,
    side       TEXT NOT NULL,  -- 'hub' or 'target'
    source_id  TEXT NOT NULL,  -- the item being copied
    title      TEXT NOT NULL,
    started_at TEXT NOT NULL,
    PRIMARY KEY (pair_id, side, source_id)
);
```

Each side keeps its own baseline (`hub_*` and `target_*`), recorded from what that side returned after the last write. A side has changed only if its current values differ from its own baseline. Keeping the baselines separate matters if a target normalizes text, for example trimming whitespace: comparing Skylight's stored label against the Reminders title would then show a difference on every run and keep rewriting the item forever. Comparing each side against what it last returned avoids that.

Every write commits its own transaction right after the API call returns, so a crash loses at most the one in-flight item.

## Sync algorithm

For each pair, independently:

1. Read every item on both sides. If either read fails, stop this pair. An error never means "empty list".
1. Recover any `pending_create` rows (see [Crash recovery](#crash-recovery)).
1. Load the pair's links and classify each one:

   | Hub | Target | Two-way | One-way |
   | --- | --- | --- | --- |
   | changed | unchanged | Write hub values to the target. | Same. |
   | unchanged | changed | Write target values to the hub. | Write hub values back to the target. |
   | changed | changed | Merge field by field (below). | Same. |
   | gone | present | Delete the target item. | Same. |
   | present | gone, hub completed | Unlink. | Unlink. |
   | present | gone, hub incomplete | Complete the reminder. | Re-create the target item. |
   | gone | gone | Unlink. | Unlink. |

   Changes are merged field by field, comparing each side to its own baseline. If only one side changed a field, that change wins (on a one-way pair, a change made on the target is reverted). If both sides changed the same field to different values, the hub's value wins and the conflict is logged. So a title edited in Reminders and a check-off on the frame both go through, with no conflict.

1. Each incomplete hub item without a link: create it on the target and link it. Completed hub items without a link are never copied.
1. Two-way only: each incomplete target item without a link is created in the hub and linked. In one-way mode they're left alone, since people may add things on the frame directly.
1. Apply the [safety limits](#safety-limits) to the plan before anything is written.

"Gone" means absent from a complete, successful read of the list. A reminder moved to another list counts as gone from this pair.

Because changes are detected by comparing values, not timestamps or events, the sync is idempotent: a run straight after another run plans nothing. Our own writes don't count as changes on the next pass, because the baseline is updated with the result of each write. That's what prevents the feedback loop behind the 762 duplicates.

### Repeating reminders

When a target item linked to a repeating reminder is checked off, the sync completes the reminder, and EventKit moves it to its next due date instead of closing it. In the same step, the sync writes the hub's value back to the target, so the target item is unchecked again and both baselines say open. Without that, the hub would report "open, unchanged" from then on and the target item would stay checked off for every later occurrence. The rule is general: if the hub returns a different value for a field it was asked to change, that value goes to the target too.

Completing a repeating reminder can't be retried safely, because each completion adds another completed copy and moves the date on again. So when an update writes both sides, the link is saved between the two writes, with the hub's baseline set to the change as asked and the target's to the target as it is. If the target write then fails, the next sync sees the hub's new values as a hub change and retries only the target write. Before this, a target that kept rejecting the write had the reminder completed again on every retry.

Deleting the target item works the same way. The sync completes the reminder and unlinks it. The reminder is still open, so the next sync copies it to the target again, and the completed copy is never copied. Checking it off in Reminders changes nothing on the target, because the reminder stays open.

On a list pair, a repeating item comes back unchecked when its next occurrence is within the pair's [days ahead](#days-ahead). If that's later, its list item is deleted in the same step and comes back when it's in range.

### Due dates and chores

A chore pair syncs a Reminders list with one person's (one category's) chores. Each open reminder becomes one one-off chore on its due date, and dates merge field by field like titles. What's special:

- **No due date:** an open reminder with no date gets today's date as a stand-in. Its chore moves to the new today each day, and a date changed on the frame is put back, since the stand-in is never written to Reminders. Once the reminder is done it stops moving.
- **Range:** a chore appears the pair's [days ahead](#days-ahead) before it's due. The chore read covers 30 days back to 395 days ahead (the most `days_ahead` can be, plus a month), plus overdue chores. A chore moved past that on the frame looks deleted, which completes its reminder.
- **Repeating reminders:** one chore, on the current occurrence's date. Checking it off completes the reminder, EventKit moves the reminder to its next date, and the same step reopens the chore and moves it to that date. This extends the rule in [Repeating reminders](#repeating-reminders): fields the hub changed on its own while being updated go to the target too.
- **Chores the sync didn't make:** repeating and up-for-grabs chores are left alone. A one-off chore added to the person's chart on the frame is copied to Reminders with its date, on a two-way pair.

### Days ahead

Each pair has `days_ahead`: an open reminder is synced that many days before it's due, and until then it's left out, as if it weren't in the list. The default is 1 for chores, since a chore chart is about today, and 7 for lists, which get glanced at for the week ahead. It can be set from 0 (only today and overdue) to 365 in `config.toml`. Reminders with no due date are always in range.

| Case | What happens |
| --- | --- |
| A reminder due beyond the range that isn't synced yet | Nothing until it's in range, then it's created. |
| A synced reminder moved out of range in Reminders | Its target item is deleted and the link dropped. It's created again once it's back in range. |
| A repeating reminder checked off on the target whose next date is out of range | The reminder is completed once, and the target item is deleted in the same step instead of being reopened and moved. |
| A chore moved out of range on the frame | The reminder takes the new date and the chore is deleted in the same step. It comes back when it's in range. |
| A chore added out of range on the frame | It's copied to Reminders with its date, and the next sync deletes it until it's in range. |
| A target item that's gone while its reminder is out of range | The link is dropped and the reminder left open, rather than completed. The sync deletes target items itself when they move out of range, and if it's interrupted before dropping the link, the next run can't tell that from a delete on the target. |
| A reminder out of range on a pair's first sync, with an item of the same title on the target | It's kept in so it's adopted rather than copied back to Reminders as a duplicate. The next sync deletes the target item until it's in range. |

The range is strict: whoever put an item out of range, it leaves the target until it's in range again, and Reminders holds it meanwhile. Keeping a chore that someone moved out on the frame would need state remembering who set its date, since a chore the sync made out of range under a larger `days_ahead` looks the same in the link.

The cost is churn. A repeating chore gets a new chore ID each cycle (deleted, then created later) rather than moving, which is two API calls instead of one. Lowering `days_ahead` on an existing pair deletes the target items that are now out of range all at once, which can trip the [mass-change limit](#safety-limits), so the first sync after the change may need `-force`.

### First sync

A pair with no links yet may have the same things on both sides, such as "Milk" in Reminders and on the frame. Creating both would duplicate them. So on the first sync only, incomplete items are matched by exact title, one to one, and linked without writing anything. Items with ambiguous titles (the same title more than once on a side) aren't matched and fall through to the normal create rules. This is the only place titles are compared, and `remindersync sync -dry-run` shows the matches before anything is written.

### Crash recovery

Before creating an item on either side, the engine writes a `pending_create` row, and deletes it in the same transaction that writes the link. If the process dies after the API call but before that commit, the next run finds the row. It then looks for exactly one unlinked item on that side with the same title, created after `started_at` (Skylight's `created_at` or Reminders' `CreatedAt`). If it finds one, it links it, even if the source item has been deleted since: the link then has a missing side, and the normal rules delete or complete the copy instead of copying it back. The source side's baseline is the title that was copied, so an edit made to the source in the meantime still syncs. Items matched this way must have been created no more than a minute before `started_at`, to allow for clock skew between the Mac and the target. If it finds none, it drops the row and the normal rules create the item again. If it finds more than one, it drops the row, links nothing, and logs a warning. The worst case is one duplicate item per crash, never a loop.

## Safety limits

- **Dry run.** `remindersync sync -dry-run` prints the plan for each pair and writes nothing.
- **Mass-change limit.** If a pair's plan deletes more than `max(5, 25%)` of its linked target items, or completes that many hub items because their target items were deleted, the pair is skipped and reported. `sync -force` runs it anyway. For `Groceries` with, say, 20 active links, that's anything over 5. Clearing a whole list on the frame trips it, which is the point. Checking items off on either side doesn't count, since that's ordinary use.
- **Reads are all or nothing.** An error, a 5xx or a timeout aborts the pair. A missing list aborts the pair. Neither is ever read as "everything was deleted".
- **One process at a time.** An exclusive `flock` on `sync.lock` next to the database stops the daemon and a manual `sync` from running at once. The OS drops the lock if the process dies. A dry run writes nothing, so it doesn't take the lock.
- **No writes to fields a target lacks.** Hub updates set only `Title` and `Completed`.

## Running it

`remindersync daemon` syncs every pair at start and then whenever one of these fires:

- **Reminders changes:** `WatchChanges`, debounced by 2 s, triggers a sync. EventKit only reports changes made by other processes (Reminders.app, iCloud sync) to a process whose main thread runs the Core Foundation run loop, so `main` runs the command on another goroutine and gives the main thread to the run loop (`internal/mainloop`). Without that, only the poll caught them. Our own writes trigger it too, so each change is followed by one sync that finds nothing to do. That costs one extra read of each side and keeps the loop simple.
- **Target changes:** Skylight has no push, so it's polled, 60 s after the last sync by default. That's 1,440 list reads a day for each pair. It's light, but it's still undocumented API traffic, so `-poll` changes it, down to 10 s.

Each sync reloads `config.toml`, so pairs added or removed with `remindersync config` apply without a restart. It takes `sync.lock` for just that sync, so a manual `sync` can run between the daemon's. If the lock is held, it tries again in 10 s. A separate `daemon.lock`, held for the daemon's whole life, stops a second daemon from starting. On SIGTERM it lets a sync in progress finish before exiting. Its output is quiet: only pairs with changes or errors are logged, with timestamps.

The daemon records its state (waiting for Reminders access, running, access denied, stopped) and PID in a `daemon` table in `state.db`, and `status` shows it. A state of running or waiting whose PID is gone means it died without cleaning up.

It's installed as a launchd user agent (`~/Library/LaunchAgents`) rather than a system daemon, because EventKit access is granted per user and needs a login session. `remindersync install`:

1. Checks that there are pairs and a Skylight login.
1. Picks the binary the agent runs. Run from inside an app bundle (`*.app/Contents/MacOS`), including through Homebrew's symlink, it's the app's own binary, and a copy left in `bin` by an earlier install is removed. Otherwise it copies the running binary to `~/Library/Application Support/remindersync/bin/remindersync`, through a temporary file and a rename, so a fixed path stays in the plist whether it was built with `go run` or `go install`.
1. Writes `~/Library/LaunchAgents/com.zackwag.remindersync.plist` and loads it, unloading any older copy first. For the app, the plist sets `AssociatedBundleIdentifiers`, so Login Items shows the agent as ReminderSync.
1. Waits up to 2 minutes for the new daemon to report running, saying to click Allow if it's waiting on the Reminders prompt. Reports from the daemon it replaced are told apart by PID.

The agent runs at load and only in a GUI (Aqua) session, which the permission prompt needs. launchd restarts it after a crash or an error exit, 30 s apart, but not after a clean exit. That's how a refused permission stops it for good instead of prompting in a loop. stdout and stderr go to `~/Library/Logs/remindersync/daemon.log`. `remindersync uninstall` unloads the agent and removes the plist and the binary, and keeps config, state and the log.

The Skylight token comes from the Keychain through `zalando/go-keyring`, which calls `/usr/bin/security` on macOS. So the Keychain item's access list trusts `security`, not our binary, and a rebuilt daemon reads it without a Keychain prompt.

Other commands: `remindersync sync [-pair NAME] [-dry-run] [-force]` runs once, and `remindersync status` shows each pair's link count, unfinished creates, and the time, change count and any error of its last sync, from a `run` table in `state.db`.

The daemon notes its binary's identity and modification time at start and checks them after every sync. When `brew upgrade` (or `install`) swaps in a new build, it exits with an error, and launchd's `KeepAlive` restarts it on the new one 30 s later. That keeps the restart in one place rather than in a cask hook, and Homebrew's newer `postflight_steps` can't interpolate the user's UID for `launchctl kickstart` anyway.

### Distribution

Releases follow the pattern of my other Mac apps. release-please tags the release with a personal token, because tags pushed with the default token don't trigger other workflows. On the tag, `.github/workflows/release.yml` on a macOS runner:

1. Imports the Developer ID certificate into a temporary keychain.
1. Runs `scripts/build-app.sh`, which builds `remindersync` for arm64 and x86_64 with cgo, joins them with `lipo`, puts the result in `ReminderSync.app` with `packaging/Info.plist`, and signs it with the hardened runtime, a secure timestamp and `packaging/remindersync.entitlements`.
1. Notarizes the zipped app with `notarytool` and staples the ticket.
1. Uploads `ReminderSync.zip` to the release.
1. Writes `Casks/remindersync.rb` in `zackwag/homebrew-tap`, opens a PR, and merges it once the tap's checks pass.

The app is `LSUIElement`, so it has no Dock icon, and opening it from Finder just prints usage. It's a bundle so it has a stable identity: a bundle ID for TCC, a name in System Settings, and later, native notifications. The entitlements file asks for calendar access (`com.apple.security.personal-information.calendars`) in case the hardened runtime needs it for EventKit. It hasn't been tried without it.

## Dismissed alternatives

| Alternative | Why not |
| --- | --- |
| Home Assistant automations (the ledger design) | Logic split across YAML and helpers, no transactional state, and it ties a household to-do list to HA's uptime. The iCloud integration also blocks HA startup for about 3 minutes. |
| ReminderRelay | Archived. It finds items by title, turns a failed lookup into `ha_uid=""` and then deletes the reminder, and wipes notes, due dates and priority on the first sync, because its hash covers fields Skylight doesn't have. |
| iCloud web APIs (pyicloud-style) | Unofficial, needs the Apple ID password and 2FA sessions that expire, and it's the code path behind the `-20209` account lock. EventKit is supported and needs no secrets. What it costs us is needing a Mac, which the Mac Mini already covers. |
| Last-writer-wins by timestamp | Skylight has no `updated_at`. |
| Keeping a chore that's out of range when its date was set on the frame | Needs a new state column to tell it apart from a chore the sync made out of range, and makes the chart's contents depend on history. Strict is easier to predict: the chart shows what's due within `days_ahead`. |
| Only holding back reminders that aren't synced yet, so an item stays once it's on the target | Simpler, since nothing is ever deleted for being out of range. But checking off a monthly chore would put the next occurrence on the frame straight away, which is the clutter `days_ahead` is there to avoid. |
| Matching items by title | Titles change and repeat. It's how ReminderRelay lost data. Allowed only once, at first sync, with a dry run. |
| Deleting the reminder when its target item is deleted | Clearing items on the frame would destroy hub data. Completing the reminder can be undone. Deleting a reminder in the hub still deletes the target item, because the hub is the source of truth. |
| A JSON state file | Would work at this size, since 500 links is about 60 KB. But SQLite gives per-item transactions (the crash-recovery guarantee), safe concurrent reads for `status`, and a lock, for one dependency. |
| `mattn/go-sqlite3` | Needs cgo. `modernc.org/sqlite` is pure Go, so Linux CI keeps building without a C toolchain. EventKit already needs cgo on darwin, but that's the only place. |
| A Homebrew formula that builds from source | Every build would be signed ad hoc, so every `brew upgrade` would ask for Reminders access again. A cask ships one signed, notarized build. |
| Signing with a self-signed certificate | Works for TCC, but it's a certificate only this Mac trusts, and releases couldn't be notarized. The Developer ID already exists for my other apps. |
| Pairs stored in SQLite | Config is what you meant to happen, and state is a cache of what happened. Keeping them apart means `state.db` can be deleted as a last resort and rebuilt with a first sync, and `config.toml` can be read and edited by hand. |

## Open questions

- **Log rotation.** The daemon's log only grows. Quiet output keeps that slow, but a target that fails every poll adds a few lines a minute.
- **Skylight pagination.** Does `GET` on a list return every item, or cut off past some size? Only lists with a few items have been tested. The chores endpoint cuts responses off at exactly 5,000 items with no sign that it did. If lists do the same, the Skylight store has to treat a response at the cap as an error, because otherwise items past the cap would look deleted.
- **Dates written to Reminders get a time.** go-eventkit always writes hours, minutes and seconds with a due date, so a reminder that only had a date gets midnight when its chore is moved on the frame or when a chore added on the frame is copied to Reminders. Writing a date only would need a go-eventkit change.
- **Skylight `draft` items.** Items have a `draft` flag. Should drafts be skipped?
- **Skylight rate limits.** Unknown. The first sync of a big list writes sequentially, with no parallel requests.
- **`WatchChanges` latency for shared lists.** How fast do changes made on another device show up?

## Build order

1. `internal/engine` with in-memory stores and table-driven tests for every row of the classification table, first sync, crash recovery and the limits. This is where the risk is, so it comes first and gets the most tests.
1. `internal/state`, `internal/reminders` and the Skylight store.
1. `internal/config` and `remindersync config`.
1. `remindersync sync` and `status`.
1. The TCC spike, then `daemon` and `install`.
