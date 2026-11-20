# syntax=docker/dockerfile:1

ARG GO_VERSION

# The builder always runs on the build host and cross-compiles for the target
# platform, so multi-arch builds need no emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=dev

RUN echo 'nobody:x:65534:65534:Nobody:/:' > /tmp/passwd && \
    apk add --no-cache upx

WORKDIR /src

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.Version=${VERSION} -X main.Gitsha=${REVISION}" \
    ./cmd/dexd-mikrotik && upx --best --lzma dexd-mikrotik

FROM scratch
COPY --from=builder /tmp/passwd /etc/passwd
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder --chmod=555 /src/dexd-mikrotik /dexd-mikrotik

ENTRYPOINT ["/dexd-mikrotik"]
