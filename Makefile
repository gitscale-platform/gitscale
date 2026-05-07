.PHONY: build test lint lint-md lint-events lint-determinism install-hooks generate fmt

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

install-hooks:
	git config core.hooksPath .githooks
