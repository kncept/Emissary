// Package logging configures the backend-wide slog logger.
//
// Every backend log record goes through log/slog. LOG_LEVEL selects the
// minimum level that is emitted: DEBUG (-4), INFO (0), WARN (4) or ERROR (8).
// Names are case-insensitive and the numeric slog levels are accepted too.
// Unset or unrecognised values fall back to WARN. run.sh run defaults
// LOG_LEVEL to DEBUG for local development, while deployment sets ERROR unless
// LOG_LEVEL is already provided.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// EnvVar is the environment variable holding the minimum log level.
const EnvVar = "LOG_LEVEL"

// ParseLevel converts a LOG_LEVEL value to a slog.Level. Unknown and empty
// values return slog.LevelWarn.
func ParseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug", "-4":
		return slog.LevelDebug
	case "info", "0":
		return slog.LevelInfo
	case "warn", "warning", "4":
		return slog.LevelWarn
	case "error", "8":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// LevelFromEnv returns the configured minimum log level.
func LevelFromEnv() slog.Level {
	return ParseLevel(os.Getenv(EnvVar))
}

// Configure installs the process-wide default slog logger. Call it once at
// startup, before any application logging, so all packages share the same
// level filter. Standard library log output from dependencies is routed
// through slog by SetDefault as well.
func Configure() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: LevelFromEnv(),
	})))
}
