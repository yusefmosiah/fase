BINARY := bin/cagent
PREFIX := $(HOME)/.local

.PHONY: build install uninstall fmt lint test dogfood-web-desktop

build:
	go build -o $(BINARY) ./cmd/cagent

install: build
	install -m 755 $(BINARY) $(PREFIX)/bin/cagent
	mkdir -p $(HOME)/.claude/skills
	ln -sfn $(CURDIR)/skills/cagent $(HOME)/.claude/skills/cagent

uninstall:
	rm -f $(PREFIX)/bin/cagent
	rm -f $(HOME)/.claude/skills/cagent

fmt:
	gofmt -w ./cmd ./internal

lint:
	golangci-lint run ./...

test:
	go test ./...

dogfood-web-desktop: build
	./scripts/bootstrap-dogfood-web-desktop.sh
