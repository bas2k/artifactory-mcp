FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/artifactory-mcp ./cmd/artifactory-mcp
RUN mkdir -p /runtime-tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /runtime-tmp /tmp
COPY --from=build /out/artifactory-mcp /artifactory-mcp
USER 65532:65532

ENV MCP_HTTP_ADDR=0.0.0.0:8080 \
    MCP_TRANSPORT=streamable-http

EXPOSE 8080

ENTRYPOINT ["/artifactory-mcp"]
