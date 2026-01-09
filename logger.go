package strego

import (
	"github.com/sirupsen/logrus"

	"github.com/erennakbas/strego/types"
)

// Logger is the interface for logging in strego.
// It uses logrus.FieldLogger which is implemented by both *logrus.Logger and *logrus.Entry.
type Logger = types.Logger

// defaultLogger returns the default logrus logger.
func defaultLogger() Logger {
	return logrus.StandardLogger()
}
