# Pinned SDK audit

Pins: `github.com/jfrog/jfrog-client-go v1.54.0` (requires Go 1.23.7), `github.com/modelcontextprotocol/go-sdk v1.6.0` (requires Go 1.26.0). The module selects Go 1.26.0. These observations come from the downloaded tagged source, not master.

| Control | Pinned SDK behavior | Current application choice |
| --- | --- | --- |
| Authentication | `ServiceDetails.SetAccessToken` reaches `Authorization: Bearer` in HTTP client authentication | Environment token in service details; HTTP contract assertion |
| Context | `config.SetContext` reaches `http.NewRequestWithContext` in `http/httpclient/client.go` | Separate manager for each call with caller context and total deadline |
| Timeout | Configuration supports dial and overall request timeouts | Overall timeout plus request context bounds stream reading |
| Retries | Default is three; SDK retries network errors, 429, and 5xx through RetryExecutor | Explicitly zero, pending retry hardening; avoid overwritten open response streams |
| Redirects | Some native methods request redirects; POST has a separate redirect implementation | SDK `Send(..., followRedirect=false, closeBody=false, ...)` |
| Custom CA | `SetCertificatesPath` accepts a directory; system roots plus directory PEM contents | Validate selected file, copy into private temporary directory, clean up on shutdown |
| TLS | SDK creates a verified TLS client with minimum TLS 1.2 | `SetInsecureTls(false)` |
| Logging | SDK errors can include response bodies; `Output` normally writes stdout | Install SDK logger before operations; both destinations emit fixed JSON events to stderr |
| Streams | Native wrappers generally decode fully buffered responses; AQL returns an open reader | SDK authenticated streaming `Send`, cap bytes before decoding, close readers on every path |
| Native methods | Repository filtering, AQL, FileInfo, FolderInfo, GetItemProps, GetBuildInfo are present | Streaming client used to preserve raw envelopes and enforce pre-decoding bounds without hooks |
| Builds | `BuildInfoParams` supports name, number, project; no started parameter or all-build list service method | Fixed build endpoints through the SDK client |

Per the user's revised instruction, custom transports and custom retries are postponed. Input and endpoint validation remain application responsibilities. No arbitrary URL or AQL input is exposed to MCP clients. The streaming SDK API is the only authenticated HTTP path.

Future retry implementation must close each attempt's reader, retry only declared reads (including generated AQL POST), enforce a shared deadline, respect Retry-After, avoid stacked SDK retries, and verify cancellation and response limits against the actual wire requests. Do not silently enable SDK retries on the open-body path.
