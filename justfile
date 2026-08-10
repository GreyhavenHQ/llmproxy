# List available recipes.
default:
    @just --list

# Build the static binary.
build:
    CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/llmproxy ./cmd/llmproxy

# Rebuild the embedded web UI (requires node; internal/server/uidist is committed so plain `go build` works without it).
ui:
    cd ui && npm ci --no-fund --no-audit && npm run build

# Run the test suite.
test:
    go test ./...

# Run the test suite with the race detector.
race:
    go test -race -count=1 ./...

# Run the self-contained stress harness (see docs/performance.md).
stress:
    go run ./cmd/stress -requests 2000 -concurrency 100 -stream-ratio 0.5

# Build the docker image.
docker:
    docker build -t llmproxy .

# Run the linters.
lint:
    golangci-lint run

# Remove build artifacts.
clean:
    rm -rf bin
