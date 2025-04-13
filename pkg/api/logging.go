package api

import (
	"bufio"
	"fmt"
	"kpods-monitor/pkg/logger"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// setupLogger creates and configures a new logger
func setupLogger(debug bool) *logger.Logger {
	// Create a logger that writes to stdout
	log := logger.New(os.Stdout, os.Stdout, logger.Info)

	// If debug is enabled, set the minimum log level to Debug
	if debug {
		log = logger.New(os.Stdout, os.Stdout, logger.Debug)
	}

	// Set this logger as the default logger for the package
	logger.DefaultLogger = log

	return log
}

// loggingMiddleware creates a middleware that logs HTTP requests
func loggingMiddleware(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip logging middleware for WebSocket connections to avoid hijacking issues
			if websocket.IsWebSocketUpgrade(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Get client IP
			clientIP := getClientIP(r)

			// Create a response writer that captures the status code
			crw := &captureResponseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK, // Default status code
			}

			// Log the start of the request
			startTime := time.Now()

			// Call the next handler
			next.ServeHTTP(crw, r)

			// Log the request
			data := map[string]interface{}{
				"client_ip":    clientIP,
				"method":       r.Method,
				"path":         r.URL.Path,
				"status_code":  crw.statusCode,
				"elapsed_time": time.Since(startTime).String(),
			}

			// Log security events for suspicious activities
			if crw.statusCode == http.StatusUnauthorized || crw.statusCode == http.StatusForbidden {
				log.SecurityEvent("Authentication failure", data)
			} else if crw.statusCode == http.StatusTooManyRequests {
				log.SecurityEvent("Rate limit exceeded", data)
			} else if crw.statusCode >= 400 && crw.statusCode < 500 {
				log.Warn("Client error", data)
			} else if crw.statusCode >= 500 {
				log.Error("Server error", nil, data)
			} else {
				log.Debug("HTTP request", data)
			}
		})
	}
}

// securityMiddleware creates a middleware that monitors for security events
func securityMiddleware(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip security middleware for WebSocket connections to avoid hijacking issues
			if websocket.IsWebSocketUpgrade(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Get client IP
			clientIP := getClientIP(r)

			// Check for suspicious headers or query parameters
			if isSuspiciousRequest(r) {
				log.SecurityEvent("Suspicious request detected", map[string]interface{}{
					"client_ip": clientIP,
					"method":    r.Method,
					"path":      r.URL.Path,
					"headers":   filterSensitiveHeaders(r.Header),
					"query":     r.URL.RawQuery,
				})
			}

			// Call the next handler
			next.ServeHTTP(w, r)
		})
	}
}

// captureResponseWriter is a wrapper for http.ResponseWriter that captures the status code
type captureResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code and calls the underlying ResponseWriter's WriteHeader
func (crw *captureResponseWriter) WriteHeader(code int) {
	crw.statusCode = code
	crw.ResponseWriter.WriteHeader(code)
}

// Hijack implements the http.Hijacker interface to allow WebSocket upgrades
func (crw *captureResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := crw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

// isSuspiciousRequest checks for suspicious patterns in the request
func isSuspiciousRequest(r *http.Request) bool {
	// Check for SQL injection attempts
	if containsSQLInjection(r.URL.RawQuery) {
		return true
	}

	// Check for XSS attempts
	if containsXSS(r.URL.RawQuery) {
		return true
	}

	// Check for suspicious user agents
	userAgent := r.Header.Get("User-Agent")
	if isSuspiciousUserAgent(userAgent) {
		return true
	}

	// Check for path traversal attempts
	if containsPathTraversal(r.URL.Path) {
		return true
	}

	return false
}

// containsSQLInjection checks for SQL injection patterns
func containsSQLInjection(s string) bool {
	// Simple check for common SQL injection patterns
	patterns := []string{
		"'--",
		"OR 1=1",
		"DROP TABLE",
		"UNION SELECT",
		"SELECT *",
		"DELETE FROM",
		"UPDATE ",
		"INSERT INTO",
	}

	s = strings.ToUpper(s)
	for _, pattern := range patterns {
		if strings.Contains(s, strings.ToUpper(pattern)) {
			return true
		}
	}

	return false
}

// containsXSS checks for XSS patterns
func containsXSS(s string) bool {
	// Simple check for common XSS patterns
	patterns := []string{
		"<script>",
		"javascript:",
		"onerror=",
		"onload=",
		"eval(",
		"document.cookie",
		"alert(",
	}

	s = strings.ToLower(s)
	for _, pattern := range patterns {
		if strings.Contains(s, pattern) {
			return true
		}
	}

	return false
}

// isSuspiciousUserAgent checks for suspicious user agents
func isSuspiciousUserAgent(userAgent string) bool {
	// Simple check for common suspicious user agents
	patterns := []string{
		"sqlmap",
		"nikto",
		"nmap",
		"masscan",
		"zgrab",
		"gobuster",
		"dirbuster",
		"wfuzz",
		"hydra",
	}

	userAgent = strings.ToLower(userAgent)
	for _, pattern := range patterns {
		if strings.Contains(userAgent, pattern) {
			return true
		}
	}

	return false
}

// containsPathTraversal checks for path traversal attempts
func containsPathTraversal(path string) bool {
	// Simple check for common path traversal patterns
	patterns := []string{
		"../",
		"..\\",
		"/..",
		"\\..",
		"/etc/passwd",
		"c:\\windows",
		"cmd.exe",
		"command.com",
	}

	path = strings.ToLower(path)
	for _, pattern := range patterns {
		if strings.Contains(path, pattern) {
			return true
		}
	}

	return false
}

// filterSensitiveHeaders removes sensitive information from headers
func filterSensitiveHeaders(headers http.Header) http.Header {
	filtered := make(http.Header)
	for k, v := range headers {
		// Skip sensitive headers
		if k == "Authorization" || k == "Cookie" || k == "Set-Cookie" {
			filtered.Set(k, "[REDACTED]")
		} else {
			filtered[k] = v
		}
	}
	return filtered
}
