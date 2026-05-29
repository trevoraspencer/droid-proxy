package logging

import (
	"os"
	"strings"

	"github.com/sirupsen/logrus"

	"droid-proxy/internal/config"
)

// New builds a logrus logger configured per cfg.
func New(cfg config.Logging) *logrus.Logger {
	l := logrus.New()
	l.SetOutput(os.Stderr)
	switch strings.ToLower(cfg.Format) {
	case "json":
		l.SetFormatter(&logrus.JSONFormatter{TimestampFormat: "2006-01-02T15:04:05.000Z07:00"})
	default:
		l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: "2006-01-02T15:04:05.000Z07:00"})
	}
	lvl, err := logrus.ParseLevel(strings.ToLower(cfg.Level))
	if err != nil {
		lvl = logrus.InfoLevel
	}
	l.SetLevel(lvl)
	return l
}
