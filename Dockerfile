# syntax=docker/dockerfile:1

# -----------------------------------------------------------------------------
# Stage 1: Build cs-cloud (Go binary)
# -----------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS cs-cloud-builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Regenerate swagger docs (apidocs/ is gitignored, not present in checkout)
RUN go install github.com/swaggo/swag/cmd/swag@latest && \
    swag init -g cmd/cs-cloud/main.go -o internal/localserver/apidocs --parseDependency --parseInternal

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
# Stage 2: Extract cs binary from pre-downloaded release assets
# -----------------------------------------------------------------------------
FROM alpine AS cs-extractor

RUN apk add --no-cache tar

ARG TARGETARCH
COPY cs-binaries/ /cs-binaries/

RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) CS_ARCH="x64" ;; \
      arm64) CS_ARCH="arm64" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}"; exit 1 ;; \
    esac; \
    echo "Extracting cs for linux-${CS_ARCH}"; \
    mkdir -p /opt/cs-extract; \
    tar -xzf "/cs-binaries/cs-linux-${CS_ARCH}.tar.gz" -C /opt/cs-extract; \
    CS_BIN=$(find /opt/cs-extract -type f \( -name "cs" -o -name "cs.exe" \) | head -n1); \
    if [ -z "$CS_BIN" ]; then \
      echo "cs binary not found in archive"; ls -R /opt/cs-extract; exit 1; \
    fi; \
    cp "$CS_BIN" /opt/cs; \
    chmod +x /opt/cs

# -----------------------------------------------------------------------------
# Stage 3: Final runtime image
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim

LABEL org.opencontainers.image.source="https://github.com/XDfield/cs-cloud"
LABEL org.opencontainers.image.description="cs-cloud daemon with built-in cs CLI"

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates git && \
    rm -rf /var/lib/apt/lists/*

# Install cs binary to /usr/local/bin
COPY --from=cs-extractor /opt/cs /usr/local/bin/cs
RUN chmod +x /usr/local/bin/cs

# Install cs-cloud to /root/.costrict/bin/
RUN mkdir -p /root/.costrict/bin
COPY --from=cs-cloud-builder /build/cs-cloud /root/.costrict/bin/cs-cloud
RUN chmod +x /root/.costrict/bin/cs-cloud

ENV PATH="/root/.costrict/bin:${PATH}"

# Volume for authentication credentials (auth.json, device.json)
# Mount your local auth.json to /root/.costrict/share/auth.json
VOLUME ["/root/.costrict/share"]

ENV COSTRICT_SHARE_DIR=/root/.costrict/share

EXPOSE 8080

ENTRYPOINT ["cs-cloud"]
CMD ["start"]
