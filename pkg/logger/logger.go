// Package logger provides a simple, human-readable logging system
package logger

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level represents the severity level of a log message
type Level int

const (
	// Debug level for detailed troubleshooting
	Debug Level = iota
	// Info level for general operational information
	Info
	// Warn level for potentially harmful situations
	Warn
	// Error level for error events that might still allow the application to continue
	Error
	// Fatal level for severe error events that will lead the application to abort
	Fatal
	// Security level for security-related events
	Security
)

// String returns the string representation of the log level
func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	case Fatal:
		return "FATAL"
	case Security:
		return "SECURITY"
	default:
		return "UNKNOWN"
	}
}

// Logger is a simple logger with security event monitoring
type Logger struct {
	out         io.Writer
	securityOut io.Writer
	minLevel    Level
	mu          sync.Mutex
}

// New creates a new logger
func New(out io.Writer, securityOut io.Writer, minLevel Level) *Logger {
	if securityOut == nil {
		securityOut = out
	}
	return &Logger{
		out:         out,
		securityOut: securityOut,
		minLevel:    minLevel,
	}
}

// log logs a message at the specified level
func (l *Logger) log(level Level, msg string, data map[string]interface{}) {
	if level < l.minLevel && level != Security {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Get current time
	timestamp := time.Now().Format(time.RFC3339)

	// Determine output writer
	output := l.out
	if level == Security {
		output = l.securityOut
	}

	// Format as human-readable text
	var buf strings.Builder

	// Basic log info
	buf.WriteString(fmt.Sprintf("%s [%s] ", timestamp, colorizeLevel(level)))

	// Add message
	buf.WriteString(msg)

	// Add data if present
	if data != nil && len(data) > 0 {
		buf.WriteString("\t")
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for i, k := range keys {
			if i > 0 {
				buf.WriteString("\n\t\t\t\t ")
			}
			buf.WriteString(fmt.Sprintf("%s: %v", k, data[k]))
		}
	}

	fmt.Fprintln(output, buf.String())

	// If fatal, exit the program
	if level == Fatal {
		os.Exit(1)
	}
}

// colorizeLevel returns a colorized string representation of the log level
func colorizeLevel(level Level) string {
	switch level {
	case Debug:
		return "\033[36mDEBUG\033[0m" // Cyan
	case Info:
		return "\033[32mINFO\033[0m" // Green
	case Warn:
		return "\033[33mWARN\033[0m" // Yellow
	case Error:
		return "\033[31mERROR\033[0m" // Red
	case Fatal:
		return "\033[35mFATAL\033[0m" // Magenta
	case Security:
		return "\033[31;1mSECURITY\033[0m" // Bold Red
	default:
		return level.String()
	}
}

// Debug logs a debug message
func (l *Logger) Debug(msg string, data map[string]interface{}) {
	l.log(Debug, msg, data)
}

// Info logs an info message
func (l *Logger) Info(msg string, data map[string]interface{}) {
	l.log(Info, msg, data)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, data map[string]interface{}) {
	l.log(Warn, msg, data)
}

// Error logs an error message
func (l *Logger) Error(msg string, err error, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}
	if err != nil {
		data["error"] = err.Error()
	}
	l.log(Error, msg, data)
}

// Fatal logs a fatal message and exits the program
func (l *Logger) Fatal(msg string, err error, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}
	if err != nil {
		data["error"] = err.Error()
	}
	l.log(Fatal, msg, data)
}

// SecurityEvent logs a security event
func (l *Logger) SecurityEvent(msg string, data map[string]interface{}) {
	l.log(Security, msg, data)
}

// Global logger instance
var (
	DefaultLogger *Logger
)

// Initialize the default logger
func init() {
	// Create a default logger that writes to stdout
	DefaultLogger = New(os.Stdout, os.Stdout, Info)
}

// Standard log package compatibility functions

// Printf logs a formatted message at Info level
func Printf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	DefaultLogger.Info(msg, nil)
}

// Println logs a message at Info level
func Println(v ...interface{}) {
	msg := fmt.Sprintln(v...)
	// Remove trailing newline added by Sprintln
	msg = strings.TrimSuffix(msg, "\n")
	DefaultLogger.Info(msg, nil)
}

// Print logs a message at Info level
func Print(v ...interface{}) {
	msg := fmt.Sprint(v...)
	DefaultLogger.Info(msg, nil)
}

// Fatalf logs a formatted message at Fatal level and exits
func Fatalf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	DefaultLogger.Fatal(msg, nil, nil)
}

// Fatalln logs a message at Fatal level and exits
func Fatalln(v ...interface{}) {
	msg := fmt.Sprintln(v...)
	// Remove trailing newline added by Sprintln
	msg = strings.TrimSuffix(msg, "\n")
	DefaultLogger.Fatal(msg, nil, nil)
}

// LogFatal logs a message at Fatal level and exits
func LogFatal(v ...interface{}) {
	msg := fmt.Sprint(v...)
	DefaultLogger.Fatal(msg, nil, nil)
}

// Panicf logs a formatted message at Error level and panics
func Panicf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	DefaultLogger.Error(msg, nil, nil)
	panic(msg)
}

// Panicln logs a message at Error level and panics
func Panicln(v ...interface{}) {
	msg := fmt.Sprintln(v...)
	// Remove trailing newline added by Sprintln
	msg = strings.TrimSuffix(msg, "\n")
	DefaultLogger.Error(msg, nil, nil)
	panic(msg)
}

// Panic logs a message at Error level and panics
func Panic(v ...interface{}) {
	msg := fmt.Sprint(v...)
	DefaultLogger.Error(msg, nil, nil)
	panic(msg)
}
