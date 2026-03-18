.PHONY: build install test lint

build:
	go build -o build/cagent ./cmd/cagent

install: build
	cp build/cagent $(HOME)/.local/bin/cagent

test:
	go test ./internal/...

lint:
	go vet ./...
