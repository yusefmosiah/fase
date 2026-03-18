.PHONY: build install test lint

build:
	go build -o build/cagent ./cmd/cagent

install:
	go build -o $(HOME)/.local/bin/cagent ./cmd/cagent

test:
	go test ./internal/...

lint:
	go vet ./...
