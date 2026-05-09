.PHONY: build test lint lint-md lint-events lint-determinism lint-firecracker lint-proto install-hooks generate proto fmt

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
