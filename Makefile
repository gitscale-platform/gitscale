.PHONY: build test lint lint-md install-hooks generate fmt

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

install-hooks:
	git config core.hooksPath .githooks
