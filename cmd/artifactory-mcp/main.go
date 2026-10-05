package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"artifactory-mcp/internal/config"
	"artifactory-mcp/internal/jfrog"
	"artifactory-mcp/internal/mcpserver"
	"artifactory-mcp/internal/transport"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func run() error {
	jfrog.ConfigureLogging()
	cfg, err := config.Load(os.Getenv, os.Args[1:]...)
	if err != nil {
		return err
	}
	configureLogging(cfg.LogLevel)
	reader, err := jfrog.New(cfg)
	if err != nil {
		return err
	}
	defer reader.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	slog.Info("starting Artifactory MCP server", "version", version)
	server := mcpserver.New(reader, version, cfg.DisabledTools...)
	if cfg.Transport == "streamable-http" {
		err = transport.RunHTTP(ctx, server, cfg.HTTP)
	} else {
		err = server.Run(ctx, &mcp.StdioTransport{})
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
func main() {
	configureLogging(slog.LevelInfo)
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

func configureLogging(level slog.Level) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}
