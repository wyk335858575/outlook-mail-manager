package logging

import (
	"log/slog"
	"os"
	"time"
)

func New(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				attr.Value = slog.TimeValue(attr.Value.Time().UTC())
			}
			return attr
		},
	})
	return slog.New(handler).With("service", "outlook-mail-manager", "timezone", time.UTC.String())
}
