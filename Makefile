BIN      := bin/shardstore
VERSION  := 0.1.0
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILDTIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X shardstore/internal/version.Version=$(VERSION) \
            -X shardstore/internal/version.Commit=$(COMMIT) \
            -X shardstore/internal/version.BuildTime=$(BUILDTIME)

.PHONY: build test test-race lint bench clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/shardstore

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	golangci-lint run ./...

bench:
	go test -bench=. -benchmem ./...

clean:
	rm -rf bin data