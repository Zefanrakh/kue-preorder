#!/usr/bin/env bash
# `make session-close`: end a work session the same way every time (docs/sdlc.md).
#   1. checks nothing is left behind (uncommitted, unpushed, no PR)
#   2. prints the PR's `## Session log`, or writes it into the PR with --apply
#   3. files a lesson as an issue labelled `lesson` in this repo
# Usage: make session-close ARGS='--done "…" --tests "make check: green" --next "…" [--lesson "what · root cause · proposal"] [--apply]'
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
done_="" tests="" next="" lesson="" apply=0
while (($#)); do
  case "$1" in
    --done) done_=$2; shift 2 ;;
    --tests) tests=$2; shift 2 ;;
    --next) next=$2; shift 2 ;;
    --lesson) lesson=$2; shift 2 ;;
    --apply) apply=1; shift ;;
    *) echo "unknown option $1"; exit 2 ;;
  esac
done
branch=$(git branch --show-current)
read -r d t start_sha 2>/dev/null < .git/kp-session || true
started=${d:+$d $t}
[[ -z "${start_sha:-}" ]] && start_sha=$(git merge-base HEAD origin/main 2>/dev/null)

echo "== close kue-preorder · branch ${branch:-detached}"
warn=0
dirty=$(git status --porcelain | wc -l | tr -d ' ')
((dirty > 0)) && { echo "!  $dirty uncommitted file(s) - commit or stash before you stop"; warn=1; }
upstream=$(git rev-parse --abbrev-ref '@{u}' 2>/dev/null || true)
if [[ -z "$upstream" ]]; then
  [[ "$branch" != "main" ]] && { echo "!  branch not pushed - git push -u origin $branch"; warn=1; }
else
  unpushed=$(git rev-list --count "$upstream..HEAD")
  ((unpushed > 0)) && { echo "!  $unpushed commit(s) not pushed - git push"; warn=1; }
fi
commits=$(git rev-list --count "${start_sha:-HEAD}..HEAD" 2>/dev/null || echo 0)

pr="" pr_state="" pr_base=""
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1 && [[ "$branch" != "main" ]]; then
  IFS=$'\t' read -r pr pr_state pr_base < <(gh pr view --json number,state,baseRefName \
    -q '[.number, .state, .baseRefName] | @tsv' 2>/dev/null) || true
  [[ -z "$pr" ]] && { echo "!  no PR for this branch yet - gh pr create (the template fills the body)"; warn=1; }
  # Lesson #34: commits pushed after a merge, or a stacked PR merged into its base, never reach main.
  if [[ "$pr_state" == "MERGED" ]]; then
    git fetch -q origin main 2>/dev/null
    missing=$(git rev-list --count origin/main..HEAD 2>/dev/null || echo 0)
    ((missing > 0)) && { echo "!  PR #$pr is merged but $missing commit(s) here are not on main - open a new PR from a new branch (git switch -c <new>; gh pr create)"; warn=1; }
  fi
  if [[ "$pr_state" == "OPEN" && -n "$pr_base" && "$pr_base" != "main" ]]; then
    below=$(gh pr list --head "$pr_base" --state all -L 1 --json number,title,state \
      -q '.[] | "#\(.number) \(.title) (\(.state))"' 2>/dev/null)
    echo "!  PR #$pr is stacked on $pr_base${below:+ - PR below: $below}; merge #$pr only after GitHub shows base main"; warn=1
  fi
fi

log=$(cat <<LOG
## Session log
- date: $(date '+%Y-%m-%d %H:%M')${started:+ (started $started)}
- branch: ${branch:-detached} · commits this session: $commits
- done: ${done_:-<fill>}
- tests: ${tests:-<fill>}
- next: ${next:-<fill>}
- lesson: ${lesson:-none}
LOG
)
echo; echo "$log"; echo

if ((apply)) && [[ -n "$pr" ]]; then
  if grep -q '<fill>' <<<"$log"; then
    echo "!  not written to PR #$pr: fill --done, --tests and --next first"; warn=1
  else
    gh pr view "$pr" --json body -q .body > .git/kp-pr-body
    LOG="$log" awk '
      /^## Session log/ { print ENVIRON["LOG"]; skip=1; done=1; next }
      /^## / { skip=0 }
      !skip { print }
      END { if (!done) { print ""; print ENVIRON["LOG"] } }' .git/kp-pr-body > .git/kp-pr-body.new
    gh pr edit "$pr" --body-file .git/kp-pr-body.new >/dev/null && echo "ok Session log written to PR #$pr"
    rm -f .git/kp-pr-body .git/kp-pr-body.new
  fi
elif [[ -n "$pr" ]]; then
  echo "-> paste this into PR #$pr, or run again with --apply"
fi

# Lessons become issues labelled `lesson`; the weekly review turns them into a gate, template or rule.
if [[ -n "$lesson" && "$lesson" != "none" ]] && command -v gh >/dev/null 2>&1; then
  body=$(printf 'Branch: %s%s\nFrom: make session-close\n\n%s\n' "$branch" "${pr:+ · PR #$pr}" "$lesson")
  url=$(gh issue create --title "${lesson:0:90}" --label lesson --body "$body" 2>/dev/null) \
    && echo "ok lesson filed: $url" || echo "!  lesson not filed - add an issue with label 'lesson' by hand"
fi
rm -f .git/kp-session
((warn)) && echo "-- session closed with warnings above" || echo "-- session closed clean"
exit 0
