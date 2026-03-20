.PHONY: build install test lint

build:
	go build -o build/fase ./cmd/fase
	ln -sf fase build/cagent

install:
	go build -o $(HOME)/.local/bin/fase ./cmd/fase
	ln -sf fase $(HOME)/.local/bin/cagent

test:
	go test ./internal/...

lint:
	go vet ./...
