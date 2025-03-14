package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/muesli/termenv"
)

type Handler struct {
	h               slog.Handler
	b               *bytes.Buffer
	m               *sync.Mutex
	output          *termenv.Output
	nocolor         bool
	normalLogWriter io.Writer
	debugLogWriter  io.Writer
	normalLogFile   *os.File
	debugLogFile    *os.File
	verbose         bool // Controls whether debug messages are shown in the console
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	// Always allow all log levels to be processed
	return true
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{
		h:               h.h.WithAttrs(attrs),
		b:               h.b,
		m:               h.m,
		normalLogWriter: h.normalLogWriter,
		debugLogWriter:  h.debugLogWriter,
		output:          h.output,
		nocolor:         h.nocolor,
		normalLogFile:   h.normalLogFile,
		debugLogFile:    h.debugLogFile,
		verbose:         h.verbose,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		h:               h.h.WithGroup(name),
		b:               h.b,
		m:               h.m,
		normalLogWriter: h.normalLogWriter,
		debugLogWriter:  h.debugLogWriter,
		output:          h.output,
		nocolor:         h.nocolor,
		normalLogFile:   h.normalLogFile,
		debugLogFile:    h.debugLogFile,
		verbose:         h.verbose,
	}
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Skip debug messages in console when not in verbose mode
	shouldLogToConsole := true
	if r.Level == slog.LevelDebug && !h.verbose {
		shouldLogToConsole = false
	}

	// prepare log level string
	level := fmt.Sprintf("%5s", r.Level.String())
	p := h.output.ColorProfile()
	if !h.nocolor {
		switch r.Level {
		case slog.LevelDebug:
			level = h.output.String(level).Foreground(p.Color("12")).String()
		case slog.LevelInfo:
			level = h.output.String(level).Foreground(p.Color("10")).String()
		case slog.LevelWarn:
			level = h.output.String(level).Foreground(p.Color("11")).String()
		case slog.LevelError:
			level = h.output.String(level).Foreground(p.Color("9")).String()
		}
	}

	// prepare attrs string
	attrs, err := h.computeAttrs(ctx, r)
	if err != nil {
		return err
	}

	attrStr := ""
	if attrs != nil {
		bytes, err := json.Marshal(attrs)
		if err != nil {
			return fmt.Errorf("error when marshaling attrs: %w", err)
		}
		if h.nocolor {
			attrStr = string(bytes)
		} else {
			attrStr = h.output.String(string(bytes)).Foreground(p.Color("13")).String()
		}
	}

	// prepare time string
	timeStr := r.Time.Format("[15:04:05.000]")
	if !h.nocolor {
		timeStr = h.output.String(timeStr).Foreground(p.Color("11")).String()
	}

	// print log message to terminal (if it should be shown)
	if shouldLogToConsole {
		fmt.Printf("%s [%s] %s %s\n",
			timeStr,
			level,
			r.Message,
			attrStr,
		)
	}

	// Prepare plain text for file logging (without colors)
	plainTimeStr := r.Time.Format("[15:04:05.000]")
	plainLevel := fmt.Sprintf("[%5s]", r.Level.String())
	plainAttrStr := ""
	if attrs != nil {
		bytes, err := json.Marshal(attrs)
		if err != nil {
			return fmt.Errorf("error when marshaling attrs for file: %w", err)
		}
		plainAttrStr = string(bytes)
	}

	// Format log entry for file
	logEntry := fmt.Sprintf("%s %s %s %s\n",
		plainTimeStr,
		plainLevel,
		r.Message,
		plainAttrStr,
	)

	// Write to the normal log file (info, warn, error only)
	if r.Level >= slog.LevelInfo && h.normalLogWriter != nil {
		if _, err := fmt.Fprintf(h.normalLogWriter, logEntry); err != nil {
			return fmt.Errorf("error writing to normal log file: %w", err)
		}
	}

	// Write to the debug log file (ALL levels including debug)
	if h.debugLogWriter != nil {
		if _, err := fmt.Fprintf(h.debugLogWriter, logEntry); err != nil {
			return fmt.Errorf("error writing to debug log file: %w", err)
		}
	}

	return nil
}

// Close properly closes any open log files
func (h *Handler) Close() error {
	var normalErr, debugErr error

	// Close normal log file if it exists
	if h.normalLogFile != nil {
		normalErr = h.normalLogFile.Close()
		if normalErr != nil {
			fmt.Printf("Error closing normal log file: %v\n", normalErr)
		}
	}

	// Close debug log file if it exists
	if h.debugLogFile != nil {
		debugErr = h.debugLogFile.Close()
		if debugErr != nil {
			fmt.Printf("Error closing debug log file: %v\n", debugErr)
		}
	}

	// Return an error if either close operation failed
	if normalErr != nil {
		return normalErr
	}
	return debugErr
}

// NewHandler creates a new logging handler with the specified options
func NewHandler(opts *slog.HandlerOptions, nocolor bool, verbose bool) *Handler {
	output := termenv.NewOutput(os.Stdout)

	// if no opts are given, set default values
	if opts == nil {
		// Create new options with DEBUG level
		levelVar := new(slog.LevelVar)
		levelVar.Set(slog.LevelDebug)
		opts = &slog.HandlerOptions{
			Level: levelVar,
		}
	} else if opts.Level == nil {
		// If options exist but level is nil, set level to DEBUG
		levelVar := new(slog.LevelVar)
		levelVar.Set(slog.LevelDebug)
		opts.Level = levelVar
	}

	// create a buffer
	b := &bytes.Buffer{}

	// If terminal doesn't support ANSI256 or TrueColor, force nocolor
	profile := output.ColorProfile()
	if !(profile == termenv.TrueColor || profile == termenv.ANSI256) {
		nocolor = true
	}

	// Create log files
	normalLogWriter, debugLogWriter, normalLogFile, debugLogFile := setupLogFiles()

	handler := &Handler{
		b: b,
		h: slog.NewJSONHandler(b, &slog.HandlerOptions{
			Level:       opts.Level,
			AddSource:   opts.AddSource,
			ReplaceAttr: suppressDefaults(opts.ReplaceAttr),
		}),
		m:               &sync.Mutex{},
		output:          output,
		nocolor:         nocolor,
		normalLogWriter: normalLogWriter,
		debugLogWriter:  debugLogWriter,
		normalLogFile:   normalLogFile,
		debugLogFile:    debugLogFile,
		verbose:         verbose,
	}

	// Log a debug message to verify debug logging is working
	_ = handler.Handle(context.Background(), slog.Record{
		Time:    time.Now(),
		Level:   slog.LevelDebug,
		Message: "Debug logging initialized",
	})

	return handler
}

// setupLogFiles creates a logs directory if it doesn't exist and returns file handles
// for a normal log and a debug log, both named with the current date and time
func setupLogFiles() (normalLogWriter io.Writer, debugLogWriter io.Writer, normalLogFile, debugLogFile *os.File) {
	// Ensure logs directory exists
	logsDir := "logs"
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		fmt.Printf("Warning: failed to create logs directory: %v\n", err)
		return nil, nil, nil, nil
	}

	// Create timestamp for filenames
	timestamp := time.Now().Format("2006-01-02_150405")

	// Setup normal log file
	normalLogPath := filepath.Join(logsDir, fmt.Sprintf("%s.info.log", timestamp))
	var err error
	normalLogFile, err = os.OpenFile(normalLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("Warning: failed to create normal log file: %v\n", err)
	} else {
		fmt.Printf("Logging to file: %s\n", normalLogPath)
		normalLogWriter = normalLogFile
	}

	// Setup debug log file
	debugLogPath := filepath.Join(logsDir, fmt.Sprintf("%s.debug.log", timestamp))
	debugLogFile, err = os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("Warning: failed to create debug log file: %v\n", err)
	} else {
		fmt.Printf("Debug logging to file: %s\n", debugLogPath)
		debugLogWriter = debugLogFile
	}

	return normalLogWriter, debugLogWriter, normalLogFile, debugLogFile
}

func suppressDefaults(
	next func([]string, slog.Attr) slog.Attr,
) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey ||
			a.Key == slog.LevelKey ||
			a.Key == slog.MessageKey {
			return slog.Attr{}
		}
		if next == nil {
			return a
		}
		return next(groups, a)
	}
}

func (h *Handler) computeAttrs(
	ctx context.Context,
	r slog.Record,
) (map[string]any, error) {
	h.m.Lock()
	defer func() {
		h.b.Reset()
		h.m.Unlock()
	}()

	err := h.h.Handle(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("error when calling inner handler's Handle: %w", err)
	}

	var attrs map[string]any
	err = json.Unmarshal(h.b.Bytes(), &attrs)
	if err != nil {
		return nil, fmt.Errorf("error when unmarshaling inner handler's Handle result: %w", err)
	}

	if len(attrs) == 0 {
		return nil, nil
	}

	return attrs, nil
}
