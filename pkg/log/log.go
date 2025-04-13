// Package log provides a drop-in replacement for the standard log package
// that uses the structured logger from pkg/logger
package log

import (
	"kpods-monitor/pkg/logger"
)

// Printf logs a formatted message at Info level
func Printf(format string, v ...interface{}) {
	logger.Printf(format, v...)
}

// Println logs a message at Info level
func Println(v ...interface{}) {
	logger.Println(v...)
}

// Print logs a message at Info level
func Print(v ...interface{}) {
	logger.Print(v...)
}

// Fatalf logs a formatted message at Fatal level and exits
func Fatalf(format string, v ...interface{}) {
	logger.Fatalf(format, v...)
}

// Fatalln logs a message at Fatal level and exits
func Fatalln(v ...interface{}) {
	logger.Fatalln(v...)
}

// Fatal logs a message at Fatal level and exits
func Fatal(v ...interface{}) {
	logger.LogFatal(v...)
}

// Panicf logs a formatted message at Error level and panics
func Panicf(format string, v ...interface{}) {
	logger.Panicf(format, v...)
}

// Panicln logs a message at Error level and panics
func Panicln(v ...interface{}) {
	logger.Panicln(v...)
}

// Panic logs a message at Error level and panics
func Panic(v ...interface{}) {
	logger.Panic(v...)
}
