FROM golang:1.25-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/artifactory-mcp ./cmd/artifactory-mcp
RUN mkdir -p /runtime-tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /runtime-tmp /tmp
COPY --from=build /out/artifactory-mcp /artifactory-mcp
USER 65532:65532
ENTRYPOINT ["/artifactory-mcp"]
