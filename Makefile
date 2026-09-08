SHELL := /usr/bin/env bash
APP := stv-poll
VERSION ?= dev
PREFIX ?= .local

.PHONY: build install lint test vulncheck sync

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(APP) ./cmd/$(APP)

install: build
	mkdir -p "$(PREFIX)/bin" "$(PREFIX)/share/$(APP)"
	cp bin/$(APP) "$(PREFIX)/bin/$(APP)"
	cp -R cmd/$(APP)/templates cmd/$(APP)/static config/defaults.yaml "$(PREFIX)/share/$(APP)/"

lint:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	command -v golangci-lint >/dev/null
	golangci-lint run
	command -v biome >/dev/null
	biome check cmd/stv-poll/static
	command -v tidy >/dev/null
	go run ./cmd/stv-poll render-html | tidy -errors -quiet -

test:
	go test -race ./...

vulncheck:
	command -v govulncheck >/dev/null
	govulncheck ./...

sync:
	git add -A
	if git diff --cached --quiet; then :; else git commit -m "$${COMMIT_MESSAGE:-chore: sync}"; fi
	git pull
	git push
