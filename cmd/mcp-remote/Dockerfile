# Multi-stage build for the BuildPulse Streamable HTTP MCP server.
#
# Same shape as the platform-api Dockerfile: build static binary in a
# Go image, copy into a distroless runtime, run as nonroot. Image is
# pushed to the `mcp-remote` ECR repo and deployed by `.infra/mcp/`.

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath strips build paths, -ldflags makes the binary smaller and
# stamps the release tag (the release workflow strips "mcp-v" off the
# tag and passes it as -X internal/mcpserver.Version=<version>).
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/BuildPulseLLC/buildpulse-mcp/internal/mcpserver.Version=${VERSION}" \
    -o /out/buildpulse-mcp-remote \
    ./cmd/mcp-remote

# AWS RDS CA bundle, fetched in the build stage so we can `ADD` it
# with a chmod and copy a known-good file into the distroless runtime
# (distroless has no curl). The MONGODB_URI references
# `tlsCAFile=/app/global-bundle.pem`; without this the DocumentDB
# connection fails at TLS handshake. Same source as platform-api.
ADD --chmod=644 https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem /app/global-bundle.pem

# Distroless runtime. No shell, no package manager — just our binary
# plus the RDS CA bundle. Smaller and safer than alpine.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/buildpulse-mcp-remote /usr/local/bin/buildpulse-mcp-remote
COPY --from=build /app/global-bundle.pem /app/global-bundle.pem

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/buildpulse-mcp-remote"]
