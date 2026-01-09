//go:build logrus

package strego

import (
	"github.com/sirupsen/logrus"
)

// LogrusLogger wraps logrus.Logger to implement Logger interface.
type LogrusLogger struct {
	*logrus.Logger
}

// NewLogrusLogger creates a new LogrusLogger from logrus.Logger.
func NewLogrusLogger(l *logrus.Logger) *LogrusLogger {
	return &LogrusLogger{Logger: l}
}

// LogrusEntryLogger wraps logrus.Entry to implement Logger interface.
type LogrusEntryLogger struct {
	*logrus.Entry
}

// NewLogrusEntryLogger creates a new LogrusEntryLogger from logrus.Entry.
func NewLogrusEntryLogger(e *logrus.Entry) *LogrusEntryLogger {
	return &LogrusEntryLogger{Entry: e}
}

// Debug logs at debug level.
func (l *LogrusLogger) Debug(msg string, args ...any) {
	l.Logger.WithFields(argsToFields(args)).Debug(msg)
}

// Info logs at info level.
func (l *LogrusLogger) Info(msg string, args ...any) {
	l.Logger.WithFields(argsToFields(args)).Info(msg)
}

// Warn logs at warn level.
func (l *LogrusLogger) Warn(msg string, args ...any) {
	l.Logger.WithFields(argsToFields(args)).Warn(msg)
}

// Error logs at error level.
func (l *LogrusLogger) Error(msg string, args ...any) {
	l.Logger.WithFields(argsToFields(args)).Error(msg)
}

// Debug logs at debug level.
func (l *LogrusEntryLogger) Debug(msg string, args ...any) {
	l.Entry.WithFields(argsToFields(args)).Debug(msg)
}

// Info logs at info level.
func (l *LogrusEntryLogger) Info(msg string, args ...any) {
	l.Entry.WithFields(argsToFields(args)).Info(msg)
}

// Warn logs at warn level.
func (l *LogrusEntryLogger) Warn(msg string, args ...any) {
	l.Entry.WithFields(argsToFields(args)).Warn(msg)
}

// Error logs at error level.
func (l *LogrusEntryLogger) Error(msg string, args ...any) {
	l.Entry.WithFields(argsToFields(args)).Error(msg)
}

// argsToFields converts slog-style key-value pairs to logrus.Fields.
func argsToFields(args []any) logrus.Fields {
	fields := make(logrus.Fields)
	for i := 0; i < len(args)-1; i += 2 {
		if key, ok := args[i].(string); ok {
			fields[key] = args[i+1]
		}
	}
	return fields
}
