.PHONY: build test lint clean all release-dry-run

BINARY_NAME := trawl

build:
	go build -v -o bin/$(BINARY_NAME) ./cmd/trawl

test:
	go test -v -race ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ dist/
	go clean

# release-dry-run builds a snapshot release locally without publishing.
# Requires goreleaser: https://goreleaser.com/install/
# Use this before tagging to verify the release config produces valid artifacts.
release-dry-run:
	goreleaser release --snapshot --clean

all: lint test build
