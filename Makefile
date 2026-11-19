.PHONY: help test test-race test-int test-pkg lint

PKG ?= ./...

help:
	@echo "make test       - unit test"
	@echo "make test-race  - unit test and race detector"
	@echo "make test-int   - integration test (-tags=integration)"
	@echo "make test-pkg   - test for 1 package (eg: make test-pkg PKG=./internal/app)"
	@echo "make lint       - go vet and golangci-lint"

test:
	go test $(PKG)

test-race:
	go test -race $(PKG)

test-int:
	go test -tags=integration $(PKG)

test-pkg:
	go test -v $(PKG)

lint:
	go vet ./...
	golangci-lint run