# justfile is the primary task runner for m80 (the intentius org uses just).

binary  := "m80"
pkg     := "./..."
image   := "ghcr.io/intentius/m80"
version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X github.com/intentius/m80.Version=" + version

# List available recipes.
default:
    @just --list

# Compile all packages (the cmd/m80 binary arrives with the emulator scaffold).
build:
    go build -ldflags "{{ldflags}}" {{pkg}}

# Run the test suite.
test:
    go test {{pkg}}

# Run the test suite with the race detector.
race:
    go test -race {{pkg}}

# Lint with golangci-lint (matches CI once enabled).
lint:
    golangci-lint run {{pkg}}

# Check the operation inventory against the upstream service models.
model-watch:
    go run ./conformance/cmd/modelwatch

# gofmt check, as CI runs it.
fmt-check:
    @out=$(gofmt -l .); if [ -n "$out" ]; then echo "not gofmt-clean:"; echo "$out"; exit 1; fi

# Build the distroless image, stamped with the current version.
image:
    docker build --build-arg VERSION={{version}} -t {{image}}:{{version}} -t m80:candidate .

# The release gate, locally: build the image, run the suite against the
# container, tear it down. This is what CI does before it publishes anything.
image-check port="4290": image
    #!/usr/bin/env bash
    set -euo pipefail
    docker rm -f m80-check >/dev/null 2>&1 || true
    docker run -d --name m80-check -p {{port}}:4290 m80:candidate -build-delay 300ms >/dev/null
    trap 'docker rm -f m80-check >/dev/null 2>&1 || true' EXIT
    for _ in $(seq 1 60); do
        curl -sf http://localhost:{{port}}/_m80/health >/dev/null && break
        sleep 0.5
    done
    go run ./conformance/cmd/conformance -endpoint http://localhost:{{port}} -poll-timeout 20

# Run the conformance suite against an already-running target.
conformance endpoint="http://localhost:4290":
    go run ./conformance/cmd/conformance -endpoint {{endpoint}} -poll-timeout 20

# Cross-compile the release binaries and checksum them.
dist:
    #!/usr/bin/env bash
    set -euo pipefail
    rm -rf dist && mkdir -p dist
    for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
        os="${target%/*}"; arch="${target#*/}"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
            -ldflags "{{ldflags}}" -o "dist/m80_${os}_${arch}" ./cmd/m80
    done
    cd dist && shasum -a 256 m80_* > checksums.txt && cat checksums.txt

# Run the floci subset against any endpoint — the CFN-sufficient acceptance
# gate, documented in conformance/SUBSET-FLOCI.md. Exit status is the result.
subset-floci endpoint="http://localhost:4566":
    go run ./conformance/cmd/conformance -endpoint {{endpoint}} \
        -tags subset:floci -tier load-bearing -poll-timeout 20
