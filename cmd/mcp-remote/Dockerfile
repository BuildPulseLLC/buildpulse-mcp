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

# Distroless runtime. No shell, no package manager — just our binary
# plus the root CA bundle. Smaller and safer than alpine.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/buildpulse-mcp-remote /usr/local/bin/buildpulse-mcp-remote

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/buildpulse-mcp-remote"]
