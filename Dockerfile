# m80 ships the way mudflaps does: one static binary on distroless, no shell,
# no package manager, nothing to patch.
#
# The emulator holds everything in memory and writes nothing, so the image
# needs no volumes and the container needs no write access to its own
# filesystem. A tester who runs this in CI should be able to treat it as a
# process that happens to be in a container, not as infrastructure.

FROM golang:1.25 AS build

WORKDIR /src

# Dependencies first, so a source-only change reuses the module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# VERSION is stamped by the release workflow from the tag. An unstamped build
# reports "dev", which is how /_m80/health and -version tell a release apart
# from someone's laptop.
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

# CGO off and a static link, because distroless/static has no libc to link
# against. -s -w drops the symbol table and DWARF; the binary is a test double,
# not something anyone debugs from a core dump.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
    -ldflags "-s -w -X github.com/intentius/m80.Version=${VERSION}" \
    -o /out/m80 ./cmd/m80

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/m80 /usr/local/bin/m80

# 4290 is m80's default. Documentation only — it publishes nothing by itself.
EXPOSE 4290

# nonroot (uid 65532) comes from the base image. Nothing here needs root, and
# a test double asking for it would be a smell.
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/m80"]
# The default listen address binds all interfaces so the container is
# reachable from outside it. Override any flag by appending it to docker run.
CMD ["-addr", ":4290"]
