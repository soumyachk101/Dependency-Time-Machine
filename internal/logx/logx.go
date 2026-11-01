// Package logx provides structured logging helpers for the control plane.
// All log output goes through slog with a stable field vocabulary so
// downstream tooling can rely on key names.
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// Level names understood by New.
const (
	Debug = "DEBUG"
	Info  = "INFO"
	Warn  = "WARN"
	Error = "ERROR"
)

// FormatJSON / FormatText select the handler style.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// New builds a *slog.Logger at the given level name and output format.
// Unknown levels default to INFO; unknown formats default to JSON.
func New(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToUpper(level) {
	case Debug:
		lvl = slog.LevelDebug
	case Warn:
		lvl = slog.LevelWarn
	case Error:
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if strings.EqualFold(format, FormatText) {
		return slog.New(slog.NewTextHandler(os.Stdout, opts)).With("svc", "dtm")
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts)).With("svc", "dtm")
}

// With returns a child logger carrying extra attributes.
func With(l *slog.Logger, args ...any) *slog.Logger { return l.With(args...) }
