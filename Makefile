.PHONY: build test lint lint-md lint-events install-hooks generate fmt

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

install-hooks:
	git config core.hooksPath .githooks
