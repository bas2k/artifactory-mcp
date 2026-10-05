# Repository Guidelines

## Project Structure & Module Organization

This Go 1.25+ project implements a read-only Artifactory MCP server over stdio.

- `cmd/artifactory-mcp/`: executable entry point and subprocess tests.
- `internal/config/`: environment configuration and input validation.
- `internal/search/`: bounded AQL query construction.
- `internal/artifactory/`: typed responses, read operations, and error classification.
- `internal/jfrog/`: JFrog SDK adapter and HTTP contract tests.
- `internal/mcpserver/`: MCP tool registration and protocol tests.
- `testdata/`: JSON fixtures; `docs/SDK_AUDIT.md`: SDK constraints.
- `.github/workflows/`: CI and manual release packaging; `Dockerfile`: non-root runtime image.

## Build, Test, and Development Commands

Run commands from the repository root:

```sh
go mod download                                 # Fetch dependencies.
go build -o artifactory-mcp ./cmd/artifactory-mcp # Build the server.
go test ./...                                    # Run default tests.
go test -race ./...                              # Check for data races.
go vet ./...                                     # Run static analysis.
gofmt -w cmd internal                            # Format Go sources.
docker build -t artifactory-mcp .                 # Build the container.
```

Set `ARTIFACTORY_URL` and `ARTIFACTORY_ACCESS_TOKEN`, then run `./artifactory-mcp` with an MCP stdio client. See `README.md` for optional settings.

## Coding Style & Naming Conventions

Use standard Go formatting with tabs via `gofmt`; CI rejects unformatted sources. Keep package names lowercase, exported identifiers in PascalCase, and private identifiers in camelCase. Match existing snake_case MCP tool names and input JSON keys. Keep configuration, transport, domain logic, and tool registration in their respective packages.

## Testing Guidelines

Use Go's `testing` package, colocated `*_test.go` files, `TestXxx` functions, and descriptive `t.Run` subtests. Exercise validation, repository scoping, response limits, credential redaction, and protocol behavior when changing those paths. HTTP tests use in-memory connections; MCP tests use SDK transports. No numeric coverage threshold is configured.

Live tests require operator-provided fixtures and `ARTIFACTORY_INTEGRATION=1`; follow the README before running `go test -tags=integration -v ./internal/jfrog`.

## Commit & Pull Request Guidelines

Git history is unavailable in this checkout. Use concise imperative commit subjects, such as `Fix search pagination`. PRs should explain behavior changes, link relevant issues, report validation commands, and update documentation for configuration or tool changes. Run the CI checks above before submission.

## Security & Agent Instructions

Preserve read-only operations, TLS verification, repository allowlists, bounded responses, and token redaction. Keep stdout protocol-only; send diagnostics to stderr. Never commit credentials or `.env`.

When `.codegraph/` exists, use `codegraph explore "<symbol or question>"` or the CodeGraph MCP tool before searching or reading code. Do not create an index when absent.
