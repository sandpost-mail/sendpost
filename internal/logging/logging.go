// Package logging builds the process-wide slog logger.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger writing to w. format is "json" or anything else for text.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
