.PHONY: build test lint clean all

BINARY_NAME := trawl

build:
	go build -v -o bin/$(BINARY_NAME) ./cmd/trawl

test:
	go test -v -race ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
	go clean

all: lint test build
