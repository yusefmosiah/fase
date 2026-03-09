BINARY := bin/cagent

.PHONY: build fmt lint test

build:
	go build -o $(BINARY) ./cmd/cagent

fmt:
	gofmt -w ./cmd ./internal

lint:
	golangci-lint run ./...

test:
	go test ./...

