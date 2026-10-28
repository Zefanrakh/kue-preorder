#!/usr/bin/env bash
# `make versions` (part of `make check`): the local tools must be the versions
# .github/workflows/ci.yml pins, or the local gate and CI can disagree unnoticed (#39, #41).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
ci=.github/workflows/ci.yml

# pin <uses-pattern> <key>: the first `<key>: <value>` after the step that uses the action.
pin() {
  awk -v use="$1" -v key="$2" '
    index($0, "uses: " use) { found=1; next }
    found && $1 == key ":" { print $2; exit }' "$ci"
}

fail=0
compare() { # name, pinned, local
  if [[ -z "$2" ]]; then
    echo "!  $1: no pin found in $ci - update scripts/check-versions.sh"; fail=1
  elif [[ "${3#v}" != "${2#v}" ]]; then
    echo "!  $1: local ${3:-not installed}, CI pins $2 - install $2"; fail=1
  else
    echo "ok $1 $2"
  fi
}

compare golangci-lint "$(pin golangci/golangci-lint-action version)" \
  "$(golangci-lint version 2>/dev/null | grep -oE 'version [0-9.]+' | cut -d' ' -f2)"
compare sqlc "$(pin sqlc-dev/setup-sqlc sqlc-version)" "$(sqlc version 2>/dev/null)"
compare buf "$(pin bufbuild/buf-action version)" "$(buf --version 2>/dev/null)"
exit $fail
