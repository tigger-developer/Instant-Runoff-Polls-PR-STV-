SHELL := /usr/bin/env bash
APP := stv-poll
VERSION ?= dev
PREFIX ?= .local

.PHONY: build install lint test vulncheck sync

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(APP) ./cmd/$(APP)

install: build
	mkdir -p "$(PREFIX)/bin" "$(PREFIX)/share/$(APP)/docs"
	cp bin/$(APP) "$(PREFIX)/bin/$(APP)"
	cp -R cmd/$(APP)/templates cmd/$(APP)/static config/defaults.yaml "$(PREFIX)/share/$(APP)/"
	cp docs/$(APP)-help.md "$(PREFIX)/share/$(APP)/docs/"

lint:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	command -v golangci-lint >/dev/null
	golangci-lint run
	command -v biome >/dev/null
	biome check cmd/stv-poll/static
	command -v tidy >/dev/null
	cd cmd/stv-poll && STV_POLL_TIDY=tidy go test -run '^TestRenderedLandingPassesTidy$$'

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
