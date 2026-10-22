#!/usr/bin/env bash
# `make eod`: project state in about 20 lines, with gh and git only (no tokens).
set -uo pipefail
today=$(date +%Y-%m-%d)
echo "== Zefanrakh/kue-preorder · $today"

echo "-- open PRs (oldest first)"
gh pr list --state open --json number,title,createdAt,baseRefName,labels \
  -q 'sort_by(.createdAt)[] | "#\(.number) \(.createdAt[:10]) into \(.baseRefName) \([.labels[].name | select(startswith("risk:"))] | join(",")) \(.title)"'

echo "-- merged today"
gh pr list --state merged --search "merged:>=$today" --json number,title -q '.[] | "#\(.number) \(.title)"'

echo "-- attention"
gh issue list --state open --label blocker --json number,title -q '.[] | "blocker #\(.number) \(.title)"' 2>/dev/null
gh issue list --state open --label lesson --json number,title -q '.[] | "lesson #\(.number) \(.title)"' 2>/dev/null

echo "-- local"
git status -sb | head -5

echo "-- Dependabot alerts open"
gh api "repos/Zefanrakh/kue-preorder/dependabot/alerts?state=open&per_page=100" --jq 'length' 2>/dev/null || echo "n/a"
