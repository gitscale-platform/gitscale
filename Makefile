.PHONY: build test lint lint-md lint-events lint-determinism lint-firecracker lint-proto lint-graphql install-hooks generate proto fmt topo-up-single topo-up-quorum topo-up-full topo-down-single topo-down-quorum topo-down-full lint-test-tags

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run ./...

generate:
	go generate ./...

fmt:
	gofmt -w ./...

lint-md:
	markdownlint-cli2 "**/*.md"

lint-events:
	bash plane/data/kafka/lint-events.sh

lint-determinism:
	bash plane/workflow/lint/lint-determinism.sh

lint-firecracker:
	bash plane/workflow/lint/lint-firecracker.sh

lint-proto:
	buf lint

# lint-graphql runs the schema package tests (SDL parse + GitHub-subset
# compat diff + deprecation policy). Tied to `make test` indirectly via
# `go test ./...`; this target is the explicit "schema is healthy" gate
# called from CI as a fast lane (issue #113, ADR-017).
lint-graphql:
	go test ./plane/application/graphql/schema/...

proto:
	buf generate

install-hooks:
	git config core.hooksPath .githooks

.PHONY: temporal-up temporal-down vault-up vault-down workflow-stack-up workflow-stack-down

temporal-up:
	docker compose up -d temporal temporal-ui

temporal-down:
	docker compose stop temporal temporal-ui

vault-up:
	docker compose up -d vault

vault-down:
	docker compose stop vault

workflow-stack-up:
	docker compose up -d postgres redis temporal vault

workflow-stack-down:
	docker compose stop temporal temporal-ui vault

# --- Multi-topology test harness (issue #132) ---
# Stubs land here in #132. Real compose contents arrive in sub-issue 1.

TOPO_DIR := test/topology

define _topo_not_ready
	@echo "ERROR: topology '$(1)' not yet implemented."; \
	echo "       See sub-issue 1 of #132 (topology compose files)."; \
	exit 1
endef

topo-up-single:
	$(call _topo_not_ready,single)

topo-up-quorum:
	$(call _topo_not_ready,quorum)

topo-up-full:
	$(call _topo_not_ready,full)

topo-down-single topo-down-quorum topo-down-full:
	@true   # no-op until topo-up exists; safe to run on fresh checkouts

lint-test-tags:
	@bash scripts/lint-test-tags.sh
