GIT_TAG    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
LDFLAGS    := -X main.version=$(GIT_TAG) -X main.commit=$(GIT_COMMIT)

.PHONY: build test lint generate clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/schedulator ./cmd/schedulator

test:
	go test -race ./...

lint:
	golangci-lint run

generate:
	go generate ./...

clean:
	rm -rf bin/
