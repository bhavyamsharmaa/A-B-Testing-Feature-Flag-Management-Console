# syntax=docker/dockerfile:1.7
#
# One image for the API and the job commands (scheduler, outbox, purge-workspaces, purge-events, segment-usage):
#
#   docker build -t helios-backend:local backend/            (the build context is backend/, nothing else)
#   docker run --rm helios-backend:local commands            which commands there are
#   docker run --rm -e ... helios-backend:local              the API (default command)
#   docker run --rm -e ... helios-backend:local scheduler    a job, once, then exit
#
# What is in the final image, and nothing more: statically linked Go binaries, the CA certificates (for Supabase, SMTP
# and webhook targets), and a passwd file naming the unprivileged user. No shell, no package manager, no source, no
# tests or test data, no .env, no credentials. It runs as the numeric user 65532, not root.
#
# Pinned: the Go toolchain (the exact patch release, matching go.mod's `go 1.25.0` or newer) on Debian 12. To pin by digest
# as well, run `docker buildx imagetools inspect golang:<tag>` and append @sha256:<digest> to the FROM line.
ARG GO_VERSION=1.25.3

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
# Static, reproducible: no cgo, no host paths in the binary, no build id, time zones embedded (no tzdata package needed).
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
# Modules first, so they are cached until go.mod or go.sum changes. `go mod verify` checks them against go.sum.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN set -eux; \
    mkdir -p /out/bin; \
    for c in api scheduler outbox purgeworkspaces purgeevents segmentusage; do \
      GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags timetzdata -ldflags="-s -w -buildid=" -o /out/bin/$c ./cmd/$c; \
    done; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags timetzdata -ldflags="-s -w -buildid=" -o /out/helios ./cmd/helios; \
    printf 'helios:x:65532:65532:helios:/nonexistent:/sbin/nologin\n' > /out/passwd; \
    printf 'helios:x:65532:\n' > /out/group

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
COPY --from=build /out/helios /app/helios
COPY --from=build /out/bin /app/bin
USER 65532:65532
WORKDIR /app
# Production by default: the API refuses to start if a development-only setting is on (docs/DEPLOYMENT.md).
ENV HELIOS_ENV=production PORT=8080
EXPOSE 8080
# /healthz only says the process is alive (no dependency is checked), so a database outage never restarts a healthy container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/app/helios", "healthcheck"]
ENTRYPOINT ["/app/helios"]
CMD ["api"]
