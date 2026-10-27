# repo-template

Starter template for new zackwag repos. Handles the things a GitHub
"template repository" can't carry over on its own: branch protection,
squash-merge-only, and Conventional Commits enforcement.

## Usage

1. Create the new repo from this template:
   ```
   gh repo create OWNER/NEW-REPO --template zackwag/repo-template --public
   ```
2. Clone it, then run the setup script once from inside it:
   ```
   ./scripts/configure-repo.sh
   ```
   This applies to the new repo's default branch:
   - Squash merge only (merge commits and rebase merging disabled), with
     the PR title always used as the squash commit message
   - "Conventional Commits" required as a status check (see below)
   - Branch protection: no force pushes, no deletions, enforced for admins too
   - No required PR review (matches the account-wide default)

   If the repo has other CI, pass the job name(s) to also require as status
   checks — for example, the two lint jobs described below:
   ```
   ./scripts/configure-repo.sh OWNER/NEW-REPO "ShellCheck" "actionlint"
   ```

3. Delete or replace `.github/workflows/test.yml` — it's just a placeholder
   showing the "Test" job name convention used elsewhere in the account.

## Conventional Commits

`.github/workflows/conventional-commits.yml` enforces
[Conventional Commits](https://www.conventionalcommits.org) formatting
(`feat:`, `fix:`, `chore:`, etc.) on whatever actually lands on the default
branch:

- On a PR, it lints the **PR title** — since squash merges use the PR title
  as the commit message, that's what ends up in history.
- On a direct push to `main`/`master` (no PR), it lints the **commit
  message** instead, so pushing straight to the default branch still works
  as long as the message follows the format.

Both paths report under the same "Conventional Commits" check name, so
either one satisfies the required status check set by the setup script.

## Linting

`.github/workflows/lint.yml` lints the template's own content on every push
and PR to `main`:

- **ShellCheck** on everything under `scripts/`
- **actionlint** on the workflow files under `.github/workflows/`

Both are separate jobs so either can be required as its own status check
(see the setup script usage above). Neither is required by default —
`configure-repo.sh` only requires "Conventional Commits" unless you pass
additional job names.

## Releases

`.github/workflows/release-please.yml` runs
[release-please](https://github.com/googleapis/release-please) on every push
to `main`: it keeps a release PR up to date from Conventional Commits, and on
merge tags a GitHub Release and updates `CHANGELOG.md`.

- `release-please-config.json` defaults to `release-type: simple` (no
  package manifest assumed) with `include-component-in-tag: false`, so tags
  are plain `vX.Y.Z` rather than `<component>-vX.Y.Z`. Change `release-type`
  (e.g. to `node`) if the repo has a package manifest release-please should
  bump.
- `.release-please-manifest.json` starts at `0.0.0`; release-please bumps it
  from there based on commit types.
- For an npm package, add a `publish-npm` job gated on this workflow's
  `release_created` output (see e.g. homebridge-somneo's workflow for the
  pattern) to publish on release.

## Note on private repos

Branch protection is a GitHub Pro feature for private repositories on
personal accounts (this account is on Free). For a private repo, the script
will still apply squash-merge-only but skip branch protection with a clear
message. Public repos aren't affected by this limit.
