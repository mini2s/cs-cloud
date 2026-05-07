# syntax=docker/dockerfile:1

# -----------------------------------------------------------------------------
# Stage 1: Build cs-cloud (Go binary)
# -----------------------------------------------------------------------------
FROM golang:1.24-alpine AS cs-cloud-builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w \
    -X cs-cloud/internal/version.Version=${VERSION} \
    -X cs-cloud/internal/version.Commit=${COMMIT} \
    -X cs-cloud/internal/version.BuildTime=${BUILD_TIME}" \
    -o cs-cloud ./cmd/cs-cloud

# -----------------------------------------------------------------------------
# Stage 2: Download cs binary from GitHub releases
# -----------------------------------------------------------------------------
FROM alpine AS cs-downloader

RUN apk add --no-cache curl tar jq ca-certificates

ARG CS_VERSION=latest

ARG TARGETARCH
RUN set -eux; \
    if [ "$CS_VERSION" = "latest" ]; then \
      CS_VERSION=$(curl -fsSL https://api.github.com/repos/zgsm-sangfor/opencode/releases/latest | jq -r '.tag_name'); \
    fi; \
    # Map Docker TARGETARCH to release asset name \
    case "${TARGETARCH}" in \
      amd64) CS_ARCH="x64" ;; \
      arm64) CS_ARCH="arm64" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}"; exit 1 ;; \
    esac; \
    echo "Downloading cs ${CS_VERSION} for linux-${CS_ARCH}"; \
    curl -fsSL -o /tmp/cs-linux.tar.gz \
      "https://github.com/zgsm-sangfor/opencode/releases/download/${CS_VERSION}/cs-linux-${CS_ARCH}.tar.gz"; \
    mkdir -p /opt/cs-extract; \
    tar -xzf /tmp/cs-linux.tar.gz -C /opt/cs-extract; \
    CS_BIN=$(find /opt/cs-extract -type f \( -name "cs" -o -name "cs.exe" \) | head -n1); \
    if [ -z "$CS_BIN" ]; then \
      echo "cs binary not found in archive"; exit 1; \
    fi; \
    cp "$CS_BIN" /usr/local/bin/cs; \
    chmod +x /usr/local/bin/cs; \
    /usr/local/bin/cs --version || true

# -----------------------------------------------------------------------------
# Stage 3: Final runtime image
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim

LABEL org.opencontainers.image.source="https://github.com/XDfield/cs-cloud"
LABEL org.opencontainers.image.description="cs-cloud daemon with built-in cs CLI"

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates git && \
    rm -rf /var/lib/apt/lists/*

# Install cs binary (downloaded from GitHub releases)
COPY --from=cs-downloader /usr/local/bin/cs /usr/local/bin/cs
RUN chmod +x /usr/local/bin/cs

# Install cs-cloud binary
COPY --from=cs-cloud-builder /build/cs-cloud /usr/local/bin/cs-cloud
RUN chmod +x /usr/local/bin/cs-cloud

# Volume for authentication credentials (auth.json, device.json)
# Mount your local auth.json to /root/.costrict/share/auth.json
VOLUME ["/root/.costrict/share"]

ENV COSTRICT_SHARE_DIR=/root/.costrict/share

EXPOSE 8080

ENTRYPOINT ["cs-cloud"]
CMD ["start"]
