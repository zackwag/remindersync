#!/usr/bin/env bash
# Applies zackwag's standard repo settings to a repo generated from this
# template: squash-merge-only (with the PR title always used as the squash
# commit message, so Conventional Commits formatting actually lands in
# history), the Conventional Commits check as a required status check,
# GitHub Actions allowed to open PRs (for release-please), and branch
# protection on the default branch matching the pattern used across
# the account (no force pushes, no deletions, admins included, no required
# PR review). Because the Conventional Commits check is required, direct
# pushes to the default branch are rejected, so changes land through PRs.
# Private repos on a free plan get no branch protection (see below), so
# direct pushes still work there.
#
# Usage:
#   ./scripts/configure-repo.sh                      # infer repo from current git remote
#   ./scripts/configure-repo.sh owner/repo            # target a specific repo
#   ./scripts/configure-repo.sh owner/repo "Test"      # also require a "Test" status check
#   ./scripts/configure-repo.sh owner/repo "Test" "Lint"  # multiple required checks
#
# "Conventional Commits" is always included as a required check; pass any
# additional CI check names (must match the job's exact `name:`) as extra args.
#
# Requires: gh (authenticated), jq

set -euo pipefail

REPO="${1:-$(gh repo view --json nameWithOwner -q .nameWithOwner)}"
if [ "$#" -gt 0 ]; then shift; fi
CHECKS=("Conventional Commits" "$@")

BRANCH="$(gh api "repos/$REPO" -q .default_branch)"

echo "Configuring $REPO (default branch: $BRANCH)"

# --- Merge button: squash only, PR title becomes the commit message ---
gh api -X PATCH "repos/$REPO" \
  -F allow_squash_merge=true \
  -F allow_merge_commit=false \
  -F allow_rebase_merge=false \
  -F delete_branch_on_merge=true \
  -F squash_merge_commit_title=PR_TITLE \
  -F use_squash_pr_title_as_default=true \
  -F has_wiki=false \
  -F has_projects=false >/dev/null

# --- Security: Dependabot alerts + auto security-fix PRs, secret scanning ---
gh api -X PUT "repos/$REPO/vulnerability-alerts" >/dev/null 2>&1 || true
gh api -X PUT "repos/$REPO/automated-security-fixes" >/dev/null 2>&1 || true
gh api -X PATCH "repos/$REPO" -f 'security_and_analysis[secret_scanning][status]=enabled' >/dev/null 2>&1 || true

# --- Actions: let workflows open PRs (release-please needs this) ---
gh api -X PUT "repos/$REPO/actions/permissions/workflow" \
  -f default_workflow_permissions=read \
  -F can_approve_pull_request_reviews=true >/dev/null

# --- Branch protection ---
if [ "${#CHECKS[@]}" -gt 0 ]; then
  contexts_json=$(printf '%s\n' "${CHECKS[@]}" | jq -R . | jq -s .)
  status_checks_json="{\"strict\": false, \"contexts\": $contexts_json}"
else
  status_checks_json="null"
fi

payload=$(cat <<JSON
{
  "required_status_checks": $status_checks_json,
  "enforce_admins": true,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_linear_history": false
}
JSON
)

set +e
protect_out=$(echo "$payload" | gh api -X PUT "repos/$REPO/branches/$BRANCH/protection" --input - 2>&1)
protect_status=$?
set -e

if [ "$protect_status" -ne 0 ]; then
  if echo "$protect_out" | grep -q "Upgrade to GitHub Pro"; then
    echo "Squash-merge-only applied. Branch protection SKIPPED: private repos need GitHub Pro" \
         "(or make the repo public) to use this feature on a personal account."
    exit 0
  fi
  echo "Squash-merge-only applied. Branch protection FAILED:"
  echo "$protect_out"
  exit 1
fi

echo "Done: $REPO's $BRANCH branch is protected and squash-merge-only."
