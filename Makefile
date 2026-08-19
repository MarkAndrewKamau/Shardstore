BIN      := bin/shardstore
VERSION  := 0.1.0
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILDTIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X github.com/MarkAndrewKamau/shardstore/internal/version.Version=$(VERSION) \
            -X github.com/MarkAndrewKamau/shardstore/internal/version.Commit=$(COMMIT) \
            -X github.com/MarkAndrewKamau/shardstore/internal/version.BuildTime=$(BUILDTIME)

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