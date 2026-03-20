.PHONY: build install test lint

build:
	go build -o build/fase ./cmd/fase

install:
	go build -o $(HOME)/.local/bin/fase ./cmd/fase

test:
	go test ./internal/...

lint:
	go vet ./...
