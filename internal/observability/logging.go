// Package observability provides the process-wide logging and error boundary.
package observability

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Code string

const (
	CodeConfig    Code = "config_error"
	CodeDiscovery Code = "discovery_error"
	CodeAuth      Code = "auth_error"
	CodeProvider  Code = "provider_error"
	CodeRouting   Code = "routing_error"
	CodeStorage   Code = "storage_error"
	CodeNetwork   Code = "network_error"
	CodeInternal  Code = "internal_error"
)

// Error is the typed application boundary. Public is safe for clients; Error
// retains the wrapped cause for logs and errors.Is/errors.As.
type Error struct {
	Code     Code
	Category string
	Public   string
	Cause    error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Public
	}
	return fmt.Sprintf("%s: %v", e.Public, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(code Code, category, public string, cause error) *Error {
	return &Error{Code: code, Category: category, Public: public, Cause: cause}
}

func PublicError(err error) string {
	if err == nil {
		return ""
	}
	var appErr *Error
	if errors.As(err, &appErr) && appErr.Public != "" {
		return appErr.Public
	}
	return "operation failed"
}

func IsCode(err error, code Code) bool {
	var appErr *Error
	return errors.As(err, &appErr) && appErr.Code == code
}

func ErrorType(err error) string {
	if err == nil {
		return ""
	}
	return reflect.TypeOf(err).String()
}

var (
	mu     sync.RWMutex
	logger = slog.Default()
)

type Settings struct {
	Level  string
	Format string
	Output string
	File   string
	Color  string
}

func SetLogger(next *slog.Logger) {
	if next == nil {
		return
	}
	mu.Lock()
	logger = next
	mu.Unlock()
}

func Logger(category string) *slog.Logger {
	mu.RLock()
	base := logger
	mu.RUnlock()
	return base.With("category", category)
}

func ConfigureFromEnv(stderr io.Writer) (func() error, error) {
	settings := Settings{
		Level:  os.Getenv("GHR_LOG_LEVEL"),
		Format: os.Getenv("GHR_LOG_FORMAT"),
		Output: os.Getenv("GHR_LOG_OUTPUT"),
		File:   os.Getenv("GHR_LOG_FILE"),
		Color:  os.Getenv("GHR_LOG_COLOR"),
	}
	return Configure(stderr, settings)
}

func Configure(stderr io.Writer, settings Settings) (func() error, error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	level := parseLevel(settings.Level)
	format := strings.ToLower(strings.TrimSpace(settings.Format))
	color := colorEnabled(settings.Color, format)
	var output io.Writer
	var file *os.File
	switch strings.ToLower(strings.TrimSpace(settings.Output)) {
	case "stdout":
		output = os.Stdout
	case "file":
		if strings.TrimSpace(settings.File) == "" {
			return func() error { return nil }, fmt.Errorf("logging file output requires a file path")
		}
		settings.File = strings.TrimSpace(settings.File)
		fallthrough
	default:
		output = stderr
	}
	if strings.TrimSpace(settings.File) != "" {
		var err error
		file, err = os.OpenFile(settings.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return func() error { return nil }, fmt.Errorf("open log file: %w", err)
		}
		output = file
		color = false
	}
	options := &slog.HandlerOptions{Level: level, AddSource: os.Getenv("GHR_LOG_SOURCE") == "1"}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(output, options)
	} else {
		if color {
			output = &colorWriter{target: output}
		}
		handler = slog.NewTextHandler(output, options)
	}
	SetLogger(slog.New(handler).With("service", "ghrouter"))
	return func() error {
		if file == nil {
			return nil
		}
		return file.Close()
	}, nil
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func colorEnabled(value, format string) bool {
	if format == "json" || strings.EqualFold(value, "never") {
		return false
	}
	if strings.EqualFold(value, "always") {
		return true
	}
	return os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
}

type colorWriter struct{ target io.Writer }

func (w *colorWriter) Write(data []byte) (int, error) {
	color := "\x1b[36m"
	line := string(data)
	if strings.Contains(line, "level=ERROR") {
		color = "\x1b[31m"
	} else if strings.Contains(line, "level=WARN") {
		color = "\x1b[33m"
	} else if strings.Contains(line, "level=DEBUG") {
		color = "\x1b[90m"
	}
	return fmt.Fprintf(w.target, "%s%s\x1b[0m", color, line)
}

func Since(start time.Time) slog.Attr { return slog.Duration("duration", time.Since(start)) }
