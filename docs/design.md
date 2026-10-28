# Sync design

How ReminderSync keeps an Apple Reminders list and a target list (starting with Skylight) in step, and why it works the way it does.

## Goals

- Pair one Reminders list with one target list, set up through an interactive wizard like `rclone config`.
- Sync title and completion both ways, or one way from Reminders when the target should just mirror it.
- Let one Reminders list feed several targets.
- Never lose data or create runaway duplicates. The setup this replaces (the iPhone Companion app's Reminders Sync into Home Assistant) produced 762 copies of one Skylight chore, so this is the requirement everything else gives way to.

## Non-goals

- Syncing fields a target can't hold. Skylight list items only have a label and a status, so notes, due dates, priority, URLs, tags and alarms stay in Reminders and are never touched.
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
}

type Store interface {
    // Items returns every item in the list, completed ones included. It
    // either returns the complete list or an error, never a partial list.
    Items(ctx context.Context) ([]Item, error)
    Create(ctx context.Context, title string) (Item, error)
    // Update changes only the non-nil fields and returns the item as stored.
    Update(ctx context.Context, id string, title *string, completed *bool) (Item, error)
    Delete(ctx context.Context, id string) error
}
```

`Item` holds only the fields every store supports. When a future target can hold more, such as due dates, `Item` grows a field, and a capability set on each store says which fields to compare and write for that pair. Skylight's set is title and completion.

Packages:

- `skylight`: the API client (exists).
- `internal/reminders`: a `Store` over go-eventkit for one list ID.
- `internal/target/skylight`: a `Store` over the Skylight client for one frame and list.
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

### First sync

A pair with no links yet may have the same things on both sides, such as "Milk" in Reminders and on the frame. Creating both would duplicate them. So on the first sync only, incomplete items are matched by exact title, one to one, and linked without writing anything. Items with ambiguous titles (the same title more than once on a side) aren't matched and fall through to the normal create rules. This is the only place titles are compared, and `remindersync sync --dry-run` shows the matches before anything is written.

### Crash recovery

Before creating an item on either side, the engine writes a `pending_create` row, and deletes it in the same transaction that writes the link. If the process dies after the API call but before that commit, the next run finds the row. It then looks for exactly one unlinked item on that side with the same title, created after `started_at` (Skylight's `created_at` or Reminders' `CreatedAt`). If it finds one, it links it, even if the source item has been deleted since: the link then has a missing side, and the normal rules delete or complete the copy instead of copying it back. The source side's baseline is the title that was copied, so an edit made to the source in the meantime still syncs. Items matched this way must have been created no more than a minute before `started_at`, to allow for clock skew between the Mac and the target. If it finds none, it drops the row and the normal rules create the item again. If it finds more than one, it drops the row, links nothing, and logs a warning. The worst case is one duplicate item per crash, never a loop.

## Safety limits

- **Dry run.** `remindersync sync --dry-run` prints the plan for each pair and writes nothing.
- **Mass-change limit.** If a pair's plan deletes more than `max(5, 25%)` of its linked target items, or completes that many hub items because their target items were deleted, the pair is skipped and reported. `sync --force` runs it anyway. For `Groceries` with, say, 20 active links, that's anything over 5. Clearing a whole list on the frame trips it, which is the point. Checking items off on either side doesn't count, since that's ordinary use.
- **Reads are all or nothing.** An error, a 5xx or a timeout aborts the pair. A missing list aborts the pair. Neither is ever read as "everything was deleted".
- **One process at a time.** An exclusive `flock` on a lock file next to the database stops the daemon and a manual `sync` from running at once.
- **No writes to fields a target lacks.** Hub updates set only `Title` and `Completed`.

## Running it

`remindersync daemon` runs every pair in a loop:

- **Reminders changes:** `WatchChanges`, debounced by 2 s, triggers a sync.
- **Target changes:** Skylight has no push, so it's polled. The default is every 60 s, which is 1,440 list reads a day for each pair. That's light, but it's still undocumented API traffic, so the interval is configurable.

It's installed as a launchd user agent (`~/Library/LaunchAgents`) rather than a system daemon, because EventKit access is granted per user and needs a login session. `remindersync install` writes the plist and loads it.

Other commands: `remindersync sync [--pair NAME] [--dry-run] [--force]` runs once, and `remindersync status` shows each pair's link count, last sync and last error.

## Dismissed alternatives

| Alternative | Why not |
| --- | --- |
| Home Assistant automations (the ledger design) | Logic split across YAML and helpers, no transactional state, and it ties a household to-do list to HA's uptime. The iCloud integration also blocks HA startup for about 3 minutes. |
| ReminderRelay | Archived. It finds items by title, turns a failed lookup into `ha_uid=""` and then deletes the reminder, and wipes notes, due dates and priority on the first sync, because its hash covers fields Skylight doesn't have. |
| iCloud web APIs (pyicloud-style) | Unofficial, needs the Apple ID password and 2FA sessions that expire, and it's the code path behind the `-20209` account lock. EventKit is supported and needs no secrets. What it costs us is needing a Mac, which the Mac Mini already covers. |
| Last-writer-wins by timestamp | Skylight has no `updated_at`. |
| Matching items by title | Titles change and repeat. It's how ReminderRelay lost data. Allowed only once, at first sync, with a dry run. |
| Deleting the reminder when its target item is deleted | Clearing items on the frame would destroy hub data. Completing the reminder can be undone. Deleting a reminder in the hub still deletes the target item, because the hub is the source of truth. |
| A JSON state file | Would work at this size, since 500 links is about 60 KB. But SQLite gives per-item transactions (the crash-recovery guarantee), safe concurrent reads for `status`, and a lock, for one dependency. |
| `mattn/go-sqlite3` | Needs cgo. `modernc.org/sqlite` is pure Go, so Linux CI keeps building without a C toolchain. EventKit already needs cgo on darwin, but that's the only place. |
| Pairs stored in SQLite | Config is what you meant to happen, and state is a cache of what happened. Keeping them apart means `state.db` can be deleted as a last resort and rebuilt with a first sync, and `config.toml` can be read and edited by hand. |

## Open questions

- **TCC under launchd.** Will a bare binary started by launchd get the Reminders permission prompt, or does it need an app bundle with `NSRemindersUsageDescription`? This needs a spike before `install`.
- **Recurring reminders.** What does `Completed=true` from go-eventkit do to a recurring reminder? Reminders.app moves it to the next occurrence. A target item linked to it should probably go back to pending, but that depends on what EventKit actually does.
- **Skylight pagination.** Does `GET` on a list return every item, or cut off past some size? Only lists with a few items have been tested. The chores endpoint cuts responses off at exactly 5,000 items with no sign that it did. If lists do the same, the Skylight store has to treat a response at the cap as an error, because otherwise items past the cap would look deleted.
- **Skylight `draft` items.** Items have a `draft` flag. Should drafts be skipped?
- **Skylight rate limits.** Unknown. The first sync of a big list writes sequentially, with no parallel requests.
- **`WatchChanges` latency for shared lists.** How fast do changes made on another device show up?

## Build order

1. `internal/engine` with in-memory stores and table-driven tests for every row of the classification table, first sync, crash recovery and the limits. This is where the risk is, so it comes first and gets the most tests.
1. `internal/state`, `internal/reminders` and the Skylight store.
1. `internal/config` and `remindersync config`.
1. `remindersync sync` and `status`.
1. The TCC spike, then `daemon` and `install`.
