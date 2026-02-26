GIT_TAG    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS    := -X main.version=$(GIT_TAG) -X main.commit=$(GIT_COMMIT)

# Add GOPATH/bin to PATH for mockery
export PATH := $(shell go env GOPATH)/bin:$(PATH)

.PHONY: build build-solver test test-solver test-integration lint generate clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/schedulator ./cmd/schedulator

# Requires OR-Tools installed and CGO_ENABLED=1.
build-solver:
	CGO_ENABLED=1 go build -tags solver -ldflags "$(LDFLAGS)" -o bin/schedulator ./cmd/schedulator

test:
	CGO_ENABLED=0 go test -race ./...

# Requires OR-Tools installed.
test-solver:
	CGO_ENABLED=1 go test -race -tags solver ./...

test-integration:
	go test -v -race -tags=integration ./test/integration/...

lint:
	golangci-lint run

generate:
	go generate ./...

clean:
	rm -rf bin/
