package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/natefinch/lumberjack.v2"
)

func TestRedactingHandlerRemovesSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil)))
	logger.Info("enroll", "ToKeN", "one-time", "credential", "device-secret", "server", "https://it.example")

	got := output.String()
	if strings.Contains(got, "one-time") || strings.Contains(got, "device-secret") {
		t.Fatalf("secret leaked in log: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redacted value missing from log: %s", got)
	}
}

func TestRedactingHandlerRedactsGroupedAndBoundAttributes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil))).With("Authorization", "Bearer device-secret")
	logger.Info("enroll", slog.Group("credentials", slog.String("PASSWORD", "one-time")))

	got := output.String()
	if strings.Contains(got, "device-secret") || strings.Contains(got, "one-time") {
		t.Fatalf("secret leaked in log: %s", got)
	}
}

func TestRedactingHandlerResolvesDynamicSecretGroups(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil)))
	logger.Info("enroll", "request", dynamicSecretGroup{}, "cycle", cyclingLogValuer{}, "panic", panickingLogValuer{})

	got := output.String()
	if strings.Contains(got, "one-time") || strings.Contains(got, "device-secret") {
		t.Fatalf("secret leaked from LogValuer: %s", got)
	}
}

func TestNewConfiguresRequiredRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "agent.log")
	logger, closer, err := New(Options{Path: path, Level: slog.LevelInfo})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	logger.Info("started")

	writer, ok := closer.(*lumberjack.Logger)
	if !ok {
		t.Fatalf("closer = %T, want *lumberjack.Logger", closer)
	}
	if writer.MaxSize != 10 || writer.MaxBackups != 7 || writer.MaxAge != 14 || !writer.Compress {
		t.Fatalf("rotation = %#v, want 10 MiB, 7 backups, 14 days, compression", writer)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read log: %v", readErr)
	}
	if !strings.Contains(string(contents), "started") {
		t.Fatalf("log contents = %q", contents)
	}
}

type dynamicSecretGroup struct{}

func (dynamicSecretGroup) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("token", "one-time"),
		slog.Group("device", slog.String("credential", "device-secret")),
	)
}

type cyclingLogValuer struct{}

func (cyclingLogValuer) LogValue() slog.Value {
	return slog.AnyValue(cyclingLogValuer{})
}

type panickingLogValuer struct{}

func (panickingLogValuer) LogValue() slog.Value {
	panic("bad LogValuer")
}
