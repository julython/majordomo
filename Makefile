
default: help

# ============================================
# Setup
# ============================================

setup:  ## Setup project, install dev tools, and vendor assets
	go install gotest.tools/gotestsum@latest
	go mod tidy
	go run ./cmd/majordomo setup

dev:  ## Run the tool locally
	go run ./cmd/majordomo $(args)

analyze:  ## Run analyze on this repo
	go run ./cmd/majordomo analyze .

# ============================================
# Testing
# ============================================

test-clean:
	go clean -testcache

test:  ## Run the tests
	gotestsum --format short -- -race ./...

test-v: test-clean  ## Run the tests with verbose output
	gotestsum --format short-verbose -- -race ./...

test-dots:  ## Run the tests with dot output
	gotestsum --format dots -- -race ./...

test-cover:  ## Run tests and generate coverage report
	gotestsum --format testname -- -coverprofile=coverage.out -coverpkg=./internal/... ./...
	go tool cover -func=coverage.out | grep -v 100.0
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-watch:  ## Run the tests in watch mode
	gotestsum --watch --format testname

# ============================================
# Help
# ============================================

help:  ## Help is on the way
	@echo "Majodomo AI Companion"
	@echo ""
	@echo "Available Commands:"
	@grep -h '^[a-zA-Z]' $(MAKEFILE_LIST) | awk -F ':.*?## ' 'NF==2 {printf "   %-20s%s\n", $$1, $$2}' | sort

.PHONY: help setup dev run build push deploy test