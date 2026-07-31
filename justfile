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
