package logging

import (
	"context"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		value string
		want  slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{" debug ", slog.LevelDebug},
		{"-4", slog.LevelDebug},
		{"Info", slog.LevelInfo},
		{"0", slog.LevelInfo},
		{"WARN", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"4", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"8", slog.LevelError},
		{"", slog.LevelWarn},
		{"trace", slog.LevelWarn},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := ParseLevel(tt.value); got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestConfigureUsesEnvLevel(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	t.Setenv(EnvVar, "debug")
	Configure()

	ctx := context.Background()
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		t.Error("Debug records should be enabled with LOG_LEVEL=debug")
	}
	if !slog.Default().Enabled(ctx, slog.LevelError) {
		t.Error("Error records should be enabled with LOG_LEVEL=debug")
	}

	t.Setenv(EnvVar, "warn")
	Configure()
	if slog.Default().Enabled(ctx, slog.LevelInfo) {
		t.Error("Info records should be disabled with LOG_LEVEL=warn")
	}
	if !slog.Default().Enabled(ctx, slog.LevelWarn) {
		t.Error("Warn records should be enabled with LOG_LEVEL=warn")
	}
}

func TestLevelFromEnvDefaultsToWarn(t *testing.T) {
	t.Setenv(EnvVar, "")
	if got := LevelFromEnv(); got != slog.LevelWarn {
		t.Errorf("LevelFromEnv() = %v, want %v when LOG_LEVEL is unset", got, slog.LevelWarn)
	}
}
