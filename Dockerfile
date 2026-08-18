# syntax=docker/dockerfile:1.20

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406 AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG VCS_REF

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN test -n "$TARGETOS" && \
    test -n "$TARGETARCH" && \
    printf '%s\n' "$VERSION" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' && \
    printf '%s\n' "$VCS_REF" | grep -Eq '^[0-9a-f]{40}$' && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
      go build -buildvcs=false -trimpath \
        -ldflags "-s -w -X main.productCommit=$VCS_REF" \
        -o /out/sazanami-dvr ./cmd/sazanami-dvr && \
    test "$(/out/sazanami-dvr --version)" = "sazanami-dvr $VERSION"

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

ARG VERSION
ARG VCS_REF

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 sazanami && \
    adduser -S -D -H -u 10001 -G sazanami sazanami

LABEL org.opencontainers.image.source="https://github.com/g0ooo0gle/sazanami-dvr" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$VCS_REF" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build --chown=10001:10001 /out/sazanami-dvr /usr/local/bin/sazanami-dvr
COPY --chown=10001:10001 LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/sazanami-dvr/

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/sazanami-dvr"]
