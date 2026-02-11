.PHONY: build test lint generate clean

build:
	go build -o bin/schedulator ./cmd/schedulator

test:
	go test -race ./...

lint:
	golangci-lint run

generate:
	go generate ./...

clean:
	rm -rf bin/
