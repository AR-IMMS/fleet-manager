package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AR-IMMS/fleet-manager/internal/app"
	"github.com/AR-IMMS/fleet-manager/internal/platform/logging"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger, err := run(ctx, os.LookupEnv, os.Stderr)
	if err == nil {
		return 0
	}

	if logger == nil {
		slog.Error("fleet manager bootstrap failed", "error", err)
	} else {
		logger.Error("fleet manager stopped with an error", "error", err)
	}

	return 1
}

func run(ctx context.Context, lookup app.LookupEnv, output io.Writer) (*slog.Logger, error) {
	cfg, err := app.LoadConfig(lookup)
	if err != nil {
		return nil, fmt.Errorf("load fleet manager configuration: %w", err)
	}

	logger, err := logging.New(output, logging.Options{
		Level:   cfg.Logging.Level,
		Format:  cfg.Logging.Format,
		Service: cfg.Logging.Service,
	})
	if err != nil {
		return nil, fmt.Errorf("create fleet manager logger: %w", err)
	}

	application, err := app.NewApplication(cfg, logger)
	if err != nil {
		return logger, fmt.Errorf("construct fleet manager application: %w", err)
	}

	if err := application.Run(ctx); err != nil {
		return logger, fmt.Errorf("run fleet manager application: %w", err)
	}

	return logger, nil
}
