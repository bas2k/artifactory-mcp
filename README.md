# Artifactory MCP

A read-only MCP server written in Go, supporting stdio and Streamable HTTP. It exposes server information, artifact searches, repositories, storage metadata, properties, download statistics, and build information through ten typed tools. It uses the official MCP Go SDK v1.6.0 and jfrog-client-go v1.54.0.

## Build and run

Requires Go 1.26 or newer.

```sh
go mod download
go build -o artifactory-mcp ./cmd/artifactory-mcp
export ARTIFACTORY_URL=https://example.jfrog.io/artifactory
export ARTIFACTORY_ACCESS_TOKEN='your-access-token'
./artifactory-mcp
```

By default, the process reads MCP JSON-RPC messages from stdin and writes protocol messages to stdout. Application and sanitized SDK diagnostics go to stderr in both transports. Credentials are never tool arguments. There is no startup permission probe: access to a known repository works even when repository discovery is forbidden.

## Configuration

At startup, the server loads an optional `.env` file from its current working directory before validating configuration. Existing environment variables take precedence, including explicitly empty values; `--disable-tools` overrides both environment and `.env` values. This applies to both stdio and Streamable HTTP.

For local use, copy `.env.example` to `.env`, replace the placeholder URL and token, and run the server from that directory:

```sh
cp .env.example .env
./artifactory-mcp
```

The working directory is chosen by the shell or MCP client launching the process; it is not necessarily the executable's directory. Only `.env` is loaded automatically. A missing file is allowed; an unreadable or malformed file fails startup with a sanitized stderr diagnostic. Quote tokens containing `$` with single quotes to keep them literal. Secret dotenv files are excluded from Git and Docker build contexts.

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `MCP_LOG_LEVEL` | `info` | Minimum application and sanitized SDK log severity: `debug`, `info`, `warn`, or `error` (case-insensitive) |
| `MCP_TRANSPORT` | `stdio` | `stdio` or `streamable-http` |
| `MCP_DISABLE_TOOLS` | Empty | Comma-separated exact tool names to disable; overridden by `--disable-tools` |
| `MCP_HTTP_ADDR` | `127.0.0.1:8080` | HTTP listen address for `/mcp` and `/metrics` |
| `MCP_HTTP_AUTH_TOKEN` | Unset | Optional inbound bearer token for `/mcp`, separate from the Artifactory token; unset allows unauthenticated MCP requests on any bind address |
| `MCP_METRICS_AUTH_TOKEN` | Unset (disabled) | Optional independent bearer token for `/metrics`; unset allows unauthenticated scrapes even when MCP authentication is enabled |
| `MCP_HTTP_ALLOWED_ORIGINS` | Empty | Comma-separated exact HTTP(S) origins, such as `https://app.example.com`; requests with an Origin header are rejected unless listed |
| `ARTIFACTORY_URL` | Required | Full service base URL, including any reverse-proxy prefix |
| `ARTIFACTORY_ACCESS_TOKEN` | Required | Bearer access token |
| `ARTIFACTORY_REPOSITORIES` | All token-visible repositories | Comma-separated exact repository keys; restricts discovery, searches, and direct artifact tools |
| `ARTIFACTORY_CA_FILE` | System roots | PEM CA file added to system trust through the SDK's certificate configuration |
| `ARTIFACTORY_REQUEST_TIMEOUT` | `30s` | Go duration; positive, up to `10m`, covering the request and response read |
| `ARTIFACTORY_RESPONSE_LIMIT` | `5242880` | Maximum upstream response bytes; positive, up to 100 MiB |
| `ARTIFACTORY_ALLOW_HTTP` | `false` | Explicit development opt-in for plain HTTP connections to Artifactory |

HTTP settings are validated only when `MCP_TRANSPORT=streamable-http`. Origins must contain only a scheme, hostname, and optional port, without a trailing slash or wildcard. Requests without an Origin header are accepted. The origin allowlist does not enable browser CORS.

Logs use JSON on stderr at the configured minimum severity. Invalid log levels fail startup. SDK messages retain their severity but replace all original diagnostic details with a fixed message, including at `debug`; explicit SDK output is treated as an error diagnostic.

URLs containing credentials, queries, fragments, or traversal are rejected. Repository names and paths preserve spaces and Unicode. Supply relative paths with unescaped segments; empty `path` is accepted only by `list_folder` for the repository root. Traversal, empty interior segments, percent escapes, backslashes, URL-shaped inputs, and control characters are rejected.

An MCP client supporting stdio can use this command configuration (replace the example token through the client's secret configuration):

```json
{
  "mcpServers": {
    "artifactory": {
      "command": "/absolute/path/artifactory-mcp",
      "env": {
        "ARTIFACTORY_URL": "https://example.jfrog.io/artifactory",
        "ARTIFACTORY_ACCESS_TOKEN": "<access-token>",
        "ARTIFACTORY_REPOSITORIES": "libs-release-local,libs-snapshot-local"
      }
    }
  }
}
```

Disable selected tools in either transport with the flag or environment variable:

```sh
./artifactory-mcp --disable-tools=list_builds,get_build_info
# Or:
MCP_DISABLE_TOOLS=list_builds,get_build_info ./artifactory-mcp
```

The list is empty by default, enabling all ten tools. The flag replaces the environment value; `--disable-tools=` explicitly enables all tools. Names are case-sensitive; surrounding whitespace and empty entries are ignored, and repeated names are harmless. Unknown names fail startup. Disabled tools are omitted from `tools/list`, and `tools/call` rejects them without contacting Artifactory. See the tool names below.

## Streamable HTTP

With the Artifactory configuration above exported, start the HTTP transport:

```sh
MCP_TRANSPORT=streamable-http ./artifactory-mcp
```

Connect a Streamable HTTP MCP client to `http://127.0.0.1:8080/mcp`. To enable inbound authentication, export `MCP_HTTP_AUTH_TOKEN` and configure the client to send `Authorization: Bearer <MCP_HTTP_AUTH_TOKEN>` on every request. Authentication is optional for all listen addresses. The server does not implement OAuth discovery or per-user credentials.

The endpoint uses stateless requests and SSE responses to POST requests. It does not issue MCP session IDs or provide a standalone GET stream, resumable streams, or server-initiated requests. A GET with the required SSE Accept header returns 405. The SDK handles protocol negotiation, JSON-RPC framing, Content-Type, and Accept validation; clients must advertise both `application/json` and `text/event-stream` for POST requests. Legacy HTTP+SSE transport is not supported.

Incoming MCP bodies are limited to 1 MiB, and at most 32 requests execute concurrently. Oversized bodies return 413; excess concurrent requests return 503 with `Retry-After: 1`. Headers have a 5-second timeout and a 16 KiB configured limit, request bodies have a 30-second read timeout, and idle connections close after 60 seconds. Tool execution retains `ARTIFACTORY_REQUEST_TIMEOUT`; there is no global HTTP write timeout to cut off SSE responses. SIGINT/SIGTERM allow active requests 10 seconds to finish, then cancel remaining work before closing the Artifactory reader. A client disconnect also cancels its tool execution.

For remote deployments, configure `MCP_HTTP_ADDR` explicitly and terminate HTTPS at a reverse proxy. Preserve POST response streaming and set proxy timeouts to accommodate the configured upstream request timeout. SDK localhost Host protection remains enabled: when a proxy connects over loopback, set its upstream Host header to the loopback listener address. Validate browser Origins independently using `MCP_HTTP_ALLOWED_ORIGINS`.

## Prometheus metrics

In Streamable HTTP mode, `GET /metrics` serves Prometheus metrics on the same listener as `/mcp` (by default, `http://127.0.0.1:8080/metrics`). Metrics are enabled automatically in this mode; stdio mode does not start an HTTP listener. Metrics authentication is disabled by default: set `MCP_METRICS_AUTH_TOKEN` to require a separate bearer token for scrapes. It is independent of `MCP_HTTP_AUTH_TOKEN`; each endpoint accepts only its own configured token. To use shared authentication, explicitly set both variables to the same value. Both endpoints share the Origin allowlist. HEAD requests are also accepted; other metrics methods return 405.

```sh
curl http://127.0.0.1:8080/metrics
# If MCP_METRICS_AUTH_TOKEN is configured:
curl -H "Authorization: Bearer ${MCP_METRICS_AUTH_TOKEN}" http://127.0.0.1:8080/metrics
```

The endpoint exports standard Go runtime (`go_*`) and process (`process_*`) metrics, plus:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `artifactory_mcp_http_requests_total` | `method`, `code` | Completed `/mcp` requests, including authentication, Origin, size, and concurrency rejections |
| `artifactory_mcp_http_request_duration_seconds` | `method` | Histogram of `/mcp` request durations, including response streaming |
| `artifactory_mcp_http_requests_in_flight` | None | `/mcp` requests currently being handled |
| `artifactory_mcp_tool_calls_total` | `tool`, `outcome` | Completed tool calls; outcome is `success` or `error`, including tool results with `isError: true` and protocol errors |
| `artifactory_mcp_tool_call_duration_seconds` | `tool`, `outcome` | Histogram of tool-call durations |

Labels contain only bounded methods, status codes, registered tool names (or `unknown`), and outcomes. Tokens, arguments, artifact paths, repository names, and error details are excluded. Histograms include buckets up to the maximum 10-minute upstream timeout. HTTP and tool series appear after traffic; counters reset on restart. Scrapes do not count as MCP requests, and remain available when MCP's concurrency limit is reached. At most two scrapes execute concurrently, with a 5-second response timeout.

For a local Prometheus instance:

```yaml
scrape_configs:
  - job_name: artifactory-mcp
    metrics_path: /metrics
    static_configs:
      - targets: ['127.0.0.1:8080']
```

If metrics authentication is enabled, configure the Prometheus job's bearer authorization using `MCP_METRICS_AUTH_TOKEN`. Use the server's reachable address when Prometheus runs on another host or in a container.

## Tools

All tools carry read-only annotations. No upload, delete, property change, promotion, administration, raw AQL, arbitrary HTTP proxy, or transitive remote search tool is registered.

| Tool | Example arguments |
| --- | --- |
| `get_server_info` | `{}` |
| `list_repositories` | `{"type":"local","package_type":"maven","project":"my-project"}` |
| `search_artifacts` | `{"repositories":["libs-release-local"],"name_pattern":"*.jar","path_pattern":"org/example/*","properties":{"build.name":"example"},"created_after":"2026-01-01T00:00:00Z","min_size":1024,"limit":100,"offset":0}` |
| `search_artifacts_sorted` | `{"repositories":["libs-release-local"],"name_pattern":"*.jar","limit":100,"offset":0}` |
| `get_artifact_info` | `{"repository":"libs-release-local","path":"org/example/example-1.0.jar"}` |
| `list_folder` | `{"repository":"libs-release-local","path":"org/example"}` |
| `get_artifact_properties` | `{"repository":"libs-release-local","path":"org/example/example-1.0.jar","keys":["build.name","build.number"]}` |
| `get_artifact_stats` | `{"repository":"libs-release-local","path":"org/example/example-1.0.jar"}` |
| `list_builds` | `{"project":"my-project","name_filter":"example","limit":100,"offset":0}` |
| `get_build_info` | `{"name":"example","number":"42","project":"my-project","started":"2026-01-01T12:00:00.000+0000","module_limit":100,"module_offset":0,"detail_limit":100,"detail_offset":0}` |

Repository `type` supports local, remote, virtual, and federated. All repository filters are optional. Search also supports `created_before`, `modified_after`, `modified_before`, and `max_size`. Property values use exact equality; name and path patterns use AQL wildcards. Property key selection happens locally after fetching the bounded properties response.

`get_server_info` reads [`api/system/version`](https://docs.jfrog.com/administration/reference/getartifactoryversion) and returns `version`, optional `revision`, `addons`, and `license`. License preserves the server's `license` field (for example, `Artifactory OSS`) and is an empty string when absent. This is a server-reported value, not a capability probe. The tool does not query the admin-only license endpoint.

Successful calls return `structuredContent` and the same JSON as text. HTTP and validation failures return `isError: true` with a JSON text object containing `category` and `message`.

| Category | Meaning |
| --- | --- |
| `invalid_input` | Invalid arguments or upstream HTTP 400/422 |
| `unauthorized` | Upstream HTTP 401 |
| `forbidden` | Repository scope violation or upstream HTTP 403 |
| `not_found` | Upstream HTTP 404 |
| `rate_limited` | Upstream HTTP 429 |
| `timed_out` | Deadline, cancellation, or upstream HTTP 408/504 |
| `unavailable` | Network, blocked redirect, malformed response, or other upstream failure |
| `response_too_large` | Upstream body exceeds the byte limit |

Upstream error bodies are suppressed. The configured token is redacted from returned JSON strings and property keys. Build responses contain only typed identity, timestamps, status, VCS, module, artifact, and dependency fields; arbitrary properties and environment variables are omitted. Exact integer sizes/counts are emitted as JSON integers, and storage sizes retain their decimal strings. Clients should decode large JSON integers with a representation that preserves precision.

## Paging and permissions

Both search tools accept the same filters, default to 100 results, and allow up to 500 per page. `search_artifacts` omits AQL sorting for compatibility with Artifactory OSS and returns results in upstream order. Ordering is not guaranteed, and offset pages can overlap or miss artifacts if ordering or repository contents change between requests. `search_artifacts_sorted` requests ascending AQL sorting by repository, path, and name; it requires sorting support and fails on OSS. Concurrent repository changes can still affect its offset paging. `has_more` means another page may exist when the requested page is full. `upstream_range` and `notices` preserve AQL metadata; `range.total` is not advertised as the complete match count.

Build names use a case-sensitive substring `name_filter` and sort by name. Build and module/detail pages slice a bounded upstream response locally (`paging: "local_output"`). Each returned module independently applies `detail_offset` and `detail_limit` to its artifacts and dependencies. An output limit does not reduce the upstream fetch. Oversized upstream collections fail instead of being silently truncated. Folder children are immediate and bounded by the response byte limit.

Repository discovery, storage, AQL, and builds can require different token permissions and Artifactory capabilities. A discovery 403 remains a permission error. Build visibility follows the token's build/project permissions independently of `ARTIFACTORY_REPOSITORIES`; use an appropriately scoped token. All HTTP clients share the server's configured Artifactory token and repository scope.

## Standard SDK transport

The current implementation uses the SDK's authenticated HTTP client directly, without custom transport hooks. Contexts and timeouts are configured per request, TLS verification stays enabled, and SDK `Send` is called with redirects disabled and bodies left open for bounded reading and closure.

Automatic retries are disabled for now. The pinned SDK's retry path can overwrite open response streams; its response-decoding wrappers also read bodies without a byte cap. Using its streaming client preserves envelopes and decimal values and supports the `started` build parameter. Custom retry handling (including Retry-After, selected transient failures, exponential backoff, and jitter) and a transport-level operation guard are deferred per the revised scope. The application still validates an explicit read operation allowlist before requesting an authenticated URL. See [the pinned SDK audit](docs/SDK_AUDIT.md).

## Tests and compatibility

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/artifactory-mcp
```

HTTP contract tests use the real JFrog adapter against `httptest.Server` using buffered in-memory connections (no local listen permissions needed); MCP tests connect the official SDK client and server and check initialization, discovery, schemas, calls, structured output, and tool errors. Streamable HTTP tests use real HTTP framing over in-memory connections and cover optional authentication, Origins, request limits, concurrent clients, cancellation, and shutdown. A subprocess test checks stdio stdout remains protocol-only.

Live tests are opt-in and only read operator-provided fixtures. Export the configuration above plus:

```sh
export ARTIFACTORY_INTEGRATION=1
export ARTIFACTORY_TEST_REPOSITORY=libs-release-local
export ARTIFACTORY_TEST_ARTIFACT=org/example/example-1.0.jar
export ARTIFACTORY_TEST_MISSING_ARTIFACT=does-not-exist.jar
export ARTIFACTORY_TEST_PROJECT=my-project
export ARTIFACTORY_TEST_BUILD_NAME=example
export ARTIFACTORY_TEST_BUILD_NUMBER=42
export ARTIFACTORY_TEST_VERSION='<installed-version>'
export ARTIFACTORY_TEST_LICENSE='<installed-license>'
# Optional: ARTIFACTORY_TEST_BUILD_STARTED and ARTIFACTORY_TEST_DISCOVERY_FORBIDDEN=1
# Supply a restricted token, at least two readable artifacts, and a build with modules.
go test -tags=integration -v ./internal/jfrog
```

No real Artifactory installation has been validated yet, so a supported server version/license range is not claimed. Record the test output and installation version/license before declaring release acceptance.

## Container and release preparation

```sh
docker build -t artifactory-mcp .
docker run --rm -i --env ARTIFACTORY_URL --env ARTIFACTORY_ACCESS_TOKEN artifactory-mcp
```

For Streamable HTTP, publish a port and bind to the container's network interface:

```sh
docker run --rm -p 127.0.0.1:8080:8080 \
  --env ARTIFACTORY_URL --env ARTIFACTORY_ACCESS_TOKEN \
  --env MCP_TRANSPORT=streamable-http --env MCP_HTTP_ADDR=0.0.0.0:8080 \
  artifactory-mcp
```

Pass `--env MCP_HTTP_AUTH_TOKEN` as well when an inbound token is exported in the host environment, and `--env MCP_METRICS_AUTH_TOKEN` when metrics authentication is enabled.

The runtime container uses a non-root user. Mount custom CA files read-only and provide `ARTIFACTORY_CA_FILE` when needed. The Dockerfile cross-compiles for the target platform, so multiarch builds do not require QEMU.

The `Package and publish release` GitHub Actions workflow runs when a GitHub release is published or when started manually. It tests the project, packages portable binaries and a local container archive as workflow artifacts, then builds and pushes the same multiarch image to Docker Hub (`bas2k/artifactory-mcp`) and GitHub Container Registry (`ghcr.io/bas2k/artifactory-mcp`), supporting `linux/amd64` and `linux/arm64`.

When triggered by a published release, the workflow also attaches the six portable binaries (Linux, macOS, and Windows on amd64 and arm64), the container archive, and `SHA256SUMS` to that release. The upload uses the automatic `GITHUB_TOKEN` with `contents: write` permission on the package job and replaces assets with matching names on reruns. Manual runs only save these files as workflow artifacts.

Configure these secrets in the GitHub repository's **Settings → Secrets and variables → Actions**:

- Secret `DOCKERHUB_USERNAME`: Docker Hub login username.
- Secret `DOCKERHUB_TOKEN`: Docker Hub access token with write access to the destination repository.

GHCR authentication uses the workflow's automatic `GITHUB_TOKEN` with `packages: write` permission on the publishing job; no additional secret is required. See [GitHub's Container registry documentation](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry) for package access settings.

Published releases use the exact release tag as the image tag and embedded binary version in both registries. Stable releases also push `latest`; prereleases do not. Manual runs use the selected commit's full SHA and do not update `latest`. Release tags must be valid Docker image tags (letters, digits, underscores, dots, or hyphens; at most 128 characters; no leading dot or hyphen).
