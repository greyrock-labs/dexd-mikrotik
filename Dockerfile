# syntax=docker/dockerfile:1

# ==============================================================================
# Build
# ==============================================================================
# Runs on the build platform and cross-compiles for the target. The final
# stage is `scratch` with no RUN steps, so multi-arch images need no QEMU --
# which the Forgejo runner cannot provide (it cannot mount binfmt_misc).
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=dev

RUN echo 'nobody:x:65534:65534:Nobody:/:' > /tmp/passwd && \
    apk add --no-cache ca-certificates upx

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY ./cmd ./cmd
COPY ./internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION} -X main.Gitsha=${REVISION}" \
    -o /out/dexd ./cmd/dexd && upx --best --lzma /out/dexd

# ==============================================================================
# Runtime
# ==============================================================================
FROM scratch AS runtime
COPY --from=builder /tmp/passwd /etc/passwd
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder --chmod=555 /out/dexd /dexd

ENTRYPOINT ["/dexd"]
