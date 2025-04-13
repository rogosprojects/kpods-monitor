package logger

import (
	"net/http"
	"strings"
	"time"
)

// LoggingMiddleware creates a middleware that logs HTTP requests
func (l *Logger) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// Get client IP
		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}

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

		// Log based on status code
		if crw.statusCode == http.StatusUnauthorized || crw.statusCode == http.StatusForbidden {
			l.SecurityEvent("Authentication failure", data)
		} else if crw.statusCode == http.StatusTooManyRequests {
			l.SecurityEvent("Rate limit exceeded", data)
		} else if crw.statusCode >= 400 && crw.statusCode < 500 {
			l.Warn("Client error", data)
		} else if crw.statusCode >= 500 {
			l.Error("Server error", nil, data)
		} else {
			l.Debug("HTTP request", data)
		}
	})
}

// SecurityMiddleware creates a middleware that monitors for security events
func (l *Logger) SecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get client IP
		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = r.RemoteAddr
		}

		// Check for suspicious headers or query parameters
		if isSuspiciousRequest(r) {
			l.SecurityEvent("Suspicious request detected", map[string]interface{}{
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
