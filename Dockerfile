# syntax=docker/dockerfile:1

# ---- Web panel ----
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Server ----
# Cross-compile on the build platform; no emulation needed for multi-arch images.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/web/dist/ internal/web/dist/
ARG VERSION=0.1.0
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X acs/internal/version.Version=${VERSION}" \
      -o /out/acs-server ./cmd/acs-server \
 && mkdir -p /out/data \
 && echo 'acs:x:65532:65532::/data:/sbin/nologin' > /out/passwd

# ---- Runtime ----
# A static binary needs nothing but CA certificates (for HTTPS webhooks).
FROM scratch
COPY --from=server /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=server /out/passwd /etc/passwd
COPY --from=server /out/acs-server /usr/local/bin/acs-server
# Named volumes inherit this directory's ownership on first use.
COPY --from=server --chown=65532:65532 /out/data /data
USER 65532:65532
ENV ACS_DATA_DIR=/data \
    ACS_LISTEN=:8080 \
    ACS_S3_LISTEN=:9000
EXPOSE 8080 9000
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/acs-server", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/acs-server"]
