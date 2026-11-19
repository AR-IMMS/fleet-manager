package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Options configures structured application logging.
//
// Level and format are fixed when the logger is constructed. Runtime
// reconfiguration is not part of this baseline.
type Options struct {
	Level   string
	Format  string
	Service string
}

// Validate rejects unsupported logging settings before startup.
//
// Accepted values are explicit to prevent spelling mistakes from silently
// selecting a different logging level or output format.
func (o Options) Validate() error {
	if _, err := parseLevel(o.Level); err != nil {
		return err
	}

	if o.Format != "json" && o.Format != "text" {
		return fmt.Errorf("log format must be json or text")
	}

	if strings.TrimSpace(o.Service) == "" {
		return fmt.Errorf("log service name is required")
	}

	return nil
}

// New constructs a logger that writes to the supplied destination.
//
// Composition owns the writer. This function does not change the global
// logger, open files, or manage output rotation.
func New(output io.Writer, cfg Options) (*slog.Logger, error) {
	if output == nil {
		return nil, fmt.Errorf("log output is required")
	}

	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	handlerOptions := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: normalizeTime,
	}

	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(output, handlerOptions)
	case "text":
		handler = slog.NewTextHandler(output, handlerOptions)
	}

	return slog.New(handler).With(
		slog.String("service", cfg.Service),
	), nil
}

// parseLevel maps the supported configuration vocabulary to slog levels.
func parseLevel(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log level must be debug, info, warn, or error")
	}
}

// normalizeTime emits the built-in record timestamp in UTC.
//
// Application attributes, including timestamps inside groups, are preserved.
func normalizeTime(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) == 0 &&
		attr.Key == slog.TimeKey &&
		attr.Value.Kind() == slog.KindTime {
		attr.Value = slog.TimeValue(attr.Value.Time().UTC())
	}

	return attr
}
