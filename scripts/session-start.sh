#!/usr/bin/env bash
# `make session-start`: where the repo and you stand, before any work (docs/sdlc.md).
# Read-only except the session marker in .git/ (never committed).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
git fetch -q origin 2>/dev/null || echo "! git fetch failed - offline? numbers below may be stale"
branch=$(git branch --show-current)
base=origin/main

printf '%s\n' "$(date '+%Y-%m-%d %H:%M') $(git rev-parse HEAD)" > .git/kp-session
echo "== kue-preorder · branch ${branch:-detached}"

dirty=$(git status --porcelain | wc -l | tr -d ' ')
read -r behind ahead < <(git rev-list --left-right --count "$base...HEAD" 2>/dev/null || echo "0 0")
echo "   uncommitted: $dirty · behind $base: $behind · ahead: $ahead"
[[ "$branch" == "main" ]] && echo "!  on main - start a branch: git switch -c m<N>.<n>-<slug>"
((behind > 0)) && [[ "$branch" == "main" ]] && echo "!  pull first: git pull --ff-only"
((behind > 0)) && [[ "$branch" != "main" ]] && echo "   main moved on by $behind commit(s): rebase if this branch needs them"

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  echo "-- open PRs"
  gh pr list --json number,title,baseRefName -q '.[] | "   #\(.number) \(.title) (into \(.baseRefName))"'
  echo "-- open issues"
  gh issue list --state open -L 10 --json number,title,labels \
    -q '.[] | "   #\(.number) \(.title) \([.labels[].name] | join(","))"'
else
  echo "-- gh not logged in: run 'gh auth login' to see PRs and issues"
fi
echo "-- next: pick the issue (Claude: /next). Close with: make session-close"
