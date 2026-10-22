# Command contract for kue-preorder (docs/sdlc.md). Run from the repo root in Git Bash:
# the recipes and the git hooks need bash. `make help` lists the targets.
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
BASE ?= origin/main
.PHONY: help setup check lint test test-changed codegen eod session-start session-close

help: ## list the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

setup: ## turn on the git hooks and install the TypeScript client's packages
	git config core.hooksPath .githooks
	cd api && npm ci --ignore-scripts

check: lint codegen test ## the full quality gate, the same as .github/workflows/ci.yml
	cd api && npm run typecheck

lint: ## go.mod tidy, vet, govulncheck, golangci-lint
	go mod tidy -diff
	go vet -tags=integration ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
	golangci-lint run

test: ## every test with the race detector; integration tests need Docker running
	go test -race -tags=integration -count=1 ./...

test-changed: ## unit tests of the packages changed against $(BASE) (pre-push)
	@pkgs=$$(git diff --name-only $(BASE)...HEAD -- '*.go' | xargs -r -n1 dirname | sort -u | sed 's#^#./#'); \
	if [ -z "$$pkgs" ]; then echo "no Go package changed against $(BASE)"; else go test -count=1 $$pkgs; fi

codegen: ## generated code is committed and the contract is clean
	sqlc diff
	buf lint
	buf format --diff --exit-code
	buf breaking --against ".git#ref=$(BASE)"
	buf generate
	@if [ -n "$$(git status --porcelain -- api/gen)" ]; then git status --short -- api/gen; \
	  echo "api/gen is out of date: run 'buf generate' and commit the result"; exit 1; fi

eod: ## project state in a few lines, without tokens
	@bash scripts/eod.sh

session-start: ## where the repo and you stand, before any work
	@bash scripts/session-start.sh

session-close: ## leftovers check + the PR's Session log; ARGS='--done "…" --tests "…" --next "…" [--lesson "…"] [--apply]'
	@bash scripts/session-close.sh $(ARGS)
