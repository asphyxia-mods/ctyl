#!/usr/bin/env bash
# Regenerates the README's image status section from the release-state branch
# and commits it to main when it changed.
# Usage: scripts/update-readme.sh <image-repository>   (e.g. ghcr.io/owner/ctyl)
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
image=${1:?image repository}
ensure_ctyl
work=$(mktemp -d)
# shellcheck disable=SC2317  # invoked via trap
cleanup() { git worktree prune; rm -rf "$work"; }
trap cleanup EXIT

git fetch -q origin release-state
git worktree add -q --detach "$work/state" FETCH_HEAD

for attempt in 1 2 3; do
  git fetch -q origin main
  tree=$work/main-$attempt
  git worktree add -q --detach "$tree" FETCH_HEAD
  ctyl status --state "$work/state/state" --readme "$tree/README.md" --image "$image"
  if git -C "$tree" diff --quiet -- README.md; then
    log "README status already current"
    exit 0
  fi
  git -C "$tree" -c user.name="github-actions[bot]" -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
    commit -q -m "docs(readme): refresh image status" -- README.md
  if git -C "$tree" push -q origin HEAD:main; then
    log "README status updated on main"
    exit 0
  fi
  log "main moved; retrying ($attempt)"
done
echo "could not update README after 3 attempts" >&2
exit 1
