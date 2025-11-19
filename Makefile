.PHONY: all build test clean setup-clusters teardown

BINARY_NAME=schedulator
SCHEDULER_BINARY=random-scheduler

all: build

build:
	@echo "Building simulator..."
	go build -o bin/$(BINARY_NAME) cmd/simulator/main.go
	@echo "Building random scheduler..."
	go build -o bin/$(SCHEDULER_BINARY) cmd/scheduler/random/main.go

test:
	go test -v ./...

clean:
	go clean
	rm -rf bin/

setup-clusters:
	@echo "Setting up Kind clusters..."
	./scripts/setup-clusters.sh

teardown:
	@echo "Tearing down clusters..."
	kind delete cluster --name cluster-1
	kind delete cluster --name cluster-2
	kind delete cluster --name cluster-3
