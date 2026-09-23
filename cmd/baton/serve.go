package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/BorjaIP/baton/internal/messages"
	"github.com/BorjaIP/baton/internal/proto"
	"github.com/BorjaIP/baton/internal/server"
)

// runServe is the "baton serve" composition root: it resolves paths, wires
// a logger and a *server.Server, and runs it until ctx is cancelled (SIGINT
// or SIGTERM, per main's signal.NotifyContext).
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Fprintln(stdout, messages.ServeUsage)
			return 0
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "%s: resolve home directory: %v\n", messages.Program, err)
		return 1
	}
	paths, err := proto.ResolvePaths(os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", messages.Program, err)
		return 1
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))

	srv := server.New(server.Config{
		Paths:   paths,
		Logger:  logger,
		Version: messages.Program,
	})

	if err := srv.Listen(); err != nil {
		if err == server.ErrAlreadyRunning {
			logger.Info("another daemon instance is already running; exiting")
			return 0
		}
		logger.Error("failed to listen", "error", err)
		return 1
	}

	if err := srv.Serve(ctx); err != nil {
		logger.Error("serve exited with an error", "error", err)
		return 1
	}
	return 0
}
