.PHONY: help setup build run-simulator run-scheduler demo teardown clean test test-unit test-e2e

help:
	@echo "Scheduler Simulator - Make targets:"
	@echo "  setup          - Setup Kind clusters and KWOK"
	@echo "  build          - Build binaries"
	@echo "  test           - Run all tests (unit and e2e)"
	@echo "  test-unit      - Run unit tests"
	@echo "  test-e2e       - Run end-to-end tests"
	@echo "  run-simulator  - Run simulator"
	@echo "  run-scheduler  - Run random scheduler"
	@echo "  demo           - Run complete demo"
	@echo "  teardown       - Delete Kind clusters"
	@echo "  clean          - Clean build artifacts"

setup:
	./scripts/setup-clusters.sh

build:
	go build -o bin/simulator cmd/simulator/main.go
	go build -o bin/random-scheduler cmd/scheduler/random/main.go

test:
	$(MAKE) test-unit
	$(MAKE) test-e2e

test-unit:
	go test ./pkg/... -v

test-e2e:
	go test ./test/e2e -v

run-simulator:
	go run cmd/simulator/main.go

run-scheduler:
	go run cmd/scheduler/random/main.go

demo: build
	@echo "Starting simulator in background..."
	./bin/simulator &
	@sleep 3
	@echo "Starting scheduler..."
	./bin/random-scheduler

teardown:
	kind delete cluster --name cluster-1 || true
	kind delete cluster --name cluster-2 || true
	kind delete cluster --name cluster-3 || true

clean:
	rm -rf bin/
	rm -rf output/