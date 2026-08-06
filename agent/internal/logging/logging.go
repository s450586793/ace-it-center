// Package logging configures the Agent's local JSONL log with secret redaction.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

const redactedValue = "[REDACTED]"

type Options struct {
	Path  string
	Level slog.Level
}

func New(options Options) (*slog.Logger, io.Closer, error) {
	if options.Path == "" {
		return nil, nil, fmt.Errorf("log path is required")
	}
	if err := os.MkdirAll(filepath.Dir(options.Path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	writer := &lumberjack.Logger{
		Filename:   options.Path,
		MaxSize:    10,
		MaxBackups: 7,
		MaxAge:     14,
		Compress:   true,
	}
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: options.Level})
	return slog.New(NewRedactingHandler(handler)), writer, nil
}

type redactingHandler struct {
	next slog.Handler
}

func NewRedactingHandler(next slog.Handler) slog.Handler {
	return &redactingHandler{next: next}
}

func (handler *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	redacted := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		redacted.AddAttrs(redactAttribute(attribute))
		return true
	})
	return handler.next.Handle(ctx, redacted)
}

func (handler *redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	return &redactingHandler{next: handler.next.WithAttrs(redactAttributes(attributes))}
}

func (handler *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: handler.next.WithGroup(name)}
}

func redactAttributes(attributes []slog.Attr) []slog.Attr {
	redacted := make([]slog.Attr, len(attributes))
	for index, attribute := range attributes {
		redacted[index] = redactAttribute(attribute)
	}
	return redacted
}

func redactAttribute(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if isSecretKey(attribute.Key) {
		return slog.String(attribute.Key, redactedValue)
	}
	if attribute.Value.Kind() != slog.KindGroup {
		return attribute
	}
	return slog.Attr{Key: attribute.Key, Value: slog.GroupValue(redactAttributes(attribute.Value.Group())...)}
}

func isSecretKey(key string) bool {
	switch strings.ToLower(key) {
	case "token", "enrollment_token", "credential", "authorization", "password":
		return true
	default:
		return false
	}
}
