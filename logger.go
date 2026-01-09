package strego

import (
	"github.com/erennakbas/strego/types"
)

// Re-export Logger types from types package for convenience.
type Logger = types.Logger
type SlogLogger = types.SlogLogger

// NewSlogLogger creates a new SlogLogger from slog.Logger.
var NewSlogLogger = types.NewSlogLogger

// defaultLogger returns the default slog-based logger.
func defaultLogger() Logger {
	return types.DefaultLogger()
}
