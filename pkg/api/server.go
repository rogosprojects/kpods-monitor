package api

import (
	"context"
	"encoding/json"
	"fmt"
	"kpods-monitor/pkg/log"
	"kpods-monitor/pkg/logger"
	"kpods-monitor/pkg/models"
	"kpods-monitor/pkg/version"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StandardError represents a standardized error response
type StandardError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse sends a standardized error response
func ErrorResponse(w http.ResponseWriter, err StandardError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.Status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    err.Code,
			"message": err.Message,
		},
	})
}

// RateLimiter provides rate limiting functionality
type RateLimiter struct {
	requests map[string][]time.Time
	mutex    sync.Mutex
	window   time.Duration
	limit    int
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(window time.Duration, limit int) *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]time.Time),
		window:   window,
		limit:    limit,
	}
}

// Allow checks if a request from the given IP is allowed
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Remove old requests
	var recent []time.Time
	for _, t := range rl.requests[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	// Update with current request
	rl.requests[ip] = append(recent, now)

	// Check if limit exceeded
	return len(rl.requests[ip]) <= rl.limit
}

// Cleanup removes old entries to prevent memory leaks
func (rl *RateLimiter) Cleanup() {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	for ip, times := range rl.requests {
		var recent []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}

		if len(recent) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = recent
		}
	}
}

// Server provides the HTTP server and API for the application dashboard
type Server struct {
	router    *mux.Router
	collector *Collector
	// InformerCollector for real-time updates
	informerCollector *InformerCollector
	config            *models.Config
	// Internal cache of applications
	applications []models.Application
	mutex        sync.RWMutex
	lastRefresh  time.Time
	startTime    time.Time

	// WebSocket-related fields
	clients       map[*websocketClient]bool
	clientsMutex  sync.Mutex
	activeClients atomic.Int32
	upgrader      websocket.Upgrader
	// Update channel for informer events
	updateCh   chan struct{}
	updateDone chan struct{}

	// Connection limits
	maxConnections int
	connsByIP      map[string]int
	connsByIPMutex sync.Mutex

	// Rate limiter
	apiRateLimiter *RateLimiter

	// LRU Cache tracking
	cacheAccessOrder []string
	accessMutex      sync.Mutex
	maxCacheSize     int

	// Logger for structured logging and security monitoring
	logger *logger.Logger
}

// websocketClient represents a connected WebSocket client
type websocketClient struct {
	conn        *websocket.Conn
	server      *Server
	ipAddress   string     // Client IP address
	connectTime time.Time  // When the client connected
	writeMutex  sync.Mutex // Mutex to protect concurrent writes to the WebSocket
}

// NewServer creates a new API server with collector
func NewServer(config *models.Config) (*Server, error) {
	// Create the collector
	collector, err := NewCollector(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create collector: %w", err)
	}

	// Create the informer collector
	informerCollector, err := NewInformerCollector(collector)
	if err != nil {
		return nil, fmt.Errorf("failed to create informer collector: %w", err)
	}

	server := &Server{
		router:            mux.NewRouter(),
		collector:         collector,
		informerCollector: informerCollector,
		config:            config,
		clients:           make(map[*websocketClient]bool),
		updateCh:          make(chan struct{}, 1),
		updateDone:        make(chan struct{}),
		startTime:         time.Now(),
		maxCacheSize:      100, // Maximum number of entries in caches
		cacheAccessOrder:  make([]string, 0, 100),
		// Initialize connection tracking
		maxConnections: 100, // Maximum concurrent connections
		connsByIP:      make(map[string]int),
		// Initialize rate limiter (60 requests per minute per IP)
		apiRateLimiter: NewRateLimiter(time.Minute, 60),
		// Initialize logger
		logger: setupLogger(config.General.Debug),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				// In production, this will be behind an ingress, so we can be more restrictive
				// Allow same origin requests always
				if r.Header.Get("Origin") == "" ||
					r.Header.Get("Origin") == "http://"+r.Host ||
					r.Header.Get("Origin") == "https://"+r.Host {
					return true
				}
				// For development, you might want to allow specific origins
				// Add your allowed origins here
				allowedOrigins := []string{
					// Add specific origins if needed
					// "https://example.com",
				}
				origin := r.Header.Get("Origin")
				for _, allowed := range allowedOrigins {
					if allowed == origin {
						return true
					}
				}
				log.Printf("Rejected WebSocket connection from origin: %s", origin)
				return false
			},
		},
	}

	// Setup routes
	server.setupRoutes()

	// Start the informer collector
	if err := informerCollector.Start(); err != nil {
		return nil, fmt.Errorf("failed to start informer collector: %w", err)
	}

	// Start the update listener
	server.startUpdateListener()

	// Do an initial data refresh
	if err := server.refreshData(); err != nil {
		log.Printf("Warning: initial data refresh failed: %v", err)
	}

	return server, nil
}

// refreshData updates the application data from Kubernetes
func (s *Server) refreshData() error {
	// Get the reason for this update if available
	updateReason := s.informerCollector.GetLastUpdateReason()
	if updateReason == "" {
		updateReason = "Manual refresh"
	}

	s.logger.Info("Refreshing application data...", map[string]interface{}{
		"reason": updateReason,
	})

	// Collect application data using the informer collector
	applications, err := s.informerCollector.CollectApplications()
	if err != nil {
		s.logger.Error("Failed to collect applications", err, map[string]interface{}{
			"last_successful_refresh": s.lastRefresh.Format(time.RFC3339),
			"reason":                  updateReason,
		})
		return fmt.Errorf("failed to collect applications: %w", err)
	}

	// Explicitly recalculate health for all applications
	for i := range applications {
		// Recalculate health to ensure zero pods are considered
		applications[i].CalculateHealth()
	}

	// Sort applications by Order for consistent display
	sort.Slice(applications, func(i, j int) bool {
		return applications[i].Order < applications[j].Order
	})

	// Update cached applications
	s.mutex.Lock()
	s.applications = applications
	s.lastRefresh = time.Now()
	s.mutex.Unlock()

	// Record access to applications for LRU caching
	for _, app := range applications {
		// Use application name as cache key
		s.updateCacheAccess(app.Name)
	}

	// Broadcast updates to websocket clients
	s.broadcastToClients()

	log.Printf("Data refresh complete. Collected %d applications.", len(applications))
	return nil
}

// securityHeadersMiddleware adds security headers to all responses
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip adding security headers for WebSocket connections to avoid interference
		if websocket.IsWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Set security headers for all other requests
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware limits the number of requests per IP
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip rate limiting for WebSocket connections
		if websocket.IsWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Get client IP
		ip := getClientIP(r)

		// Check if request is allowed
		if !s.apiRateLimiter.Allow(ip) {
			s.logger.SecurityEvent("Rate limit exceeded", map[string]interface{}{
				"client_ip": ip,
				"method":    r.Method,
				"path":      r.URL.Path,
			})
			w.Header().Set("Retry-After", "60")
			ErrorResponse(w, StandardError{
				Status:  http.StatusTooManyRequests,
				Code:    "rate_limit_exceeded",
				Message: "Rate limit exceeded. Please try again later.",
			})
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

// setupRoutes configures the router with all API endpoints
func (s *Server) setupRoutes() {
	// Get base path from config (default to empty string if not set)
	basePath := s.config.General.BasePath

	// Ensure base path starts with / if it's not empty
	if basePath != "" && !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}

	// Remove trailing slash if present
	if basePath != "" && strings.HasSuffix(basePath, "/") {
		basePath = basePath[:len(basePath)-1]
	}

	// Create a subrouter for the base path if one is configured
	var baseRouter *mux.Router
	if basePath != "" {
		s.logger.Info("Configuring application with base path", map[string]interface{}{
			"base_path": basePath,
		})

		// Add a redirect from root to the base path
		s.router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
		})

		// Handle the base path itself
		s.router.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
		})

		// Create a subrouter for the base path
		baseRouter = s.router.PathPrefix(basePath).Subrouter()
	} else {
		baseRouter = s.router
	}

	// Apply security headers middleware to all routes
	baseRouter.Use(s.securityHeadersMiddleware)

	// Apply logging middleware to all routes
	baseRouter.Use(loggingMiddleware(s.logger))

	// Apply security monitoring middleware to all routes
	baseRouter.Use(securityMiddleware(s.logger))

	// Create API subrouter with rate limiting and auth middleware
	apiRouter := baseRouter.PathPrefix("/api").Subrouter()

	// Apply rate limiting to API endpoints
	apiRouter.Use(s.rateLimitMiddleware)

	// Apply authentication middleware if enabled
	if s.config.General.Auth.Enabled {
		apiRouter.Use(s.authMiddleware)
	}

	// Start a goroutine to periodically clean up the rate limiter
	go func() {
		cleanupTicker := time.NewTicker(5 * time.Minute)
		defer cleanupTicker.Stop()

		for range cleanupTicker.C {
			s.apiRateLimiter.Cleanup()
		}
	}()

	// API routes
	apiRouter.HandleFunc("/applications", s.handleGetApplications).Methods("GET")
	apiRouter.HandleFunc("/applications/{name}", s.handleGetApplicationByName).Methods("GET")
	apiRouter.HandleFunc("/namespaces", s.handleGetNamespaces).Methods("GET")
	apiRouter.HandleFunc("/config", s.handleGetConfig).Methods("GET")

	// WebSocket endpoint - register on the apiRouter to ensure authentication is applied
	apiRouter.HandleFunc("/ws", s.handleWebSocket).Methods("GET")

	// Health and monitoring endpoints - these are registered at the root level
	// to ensure they're always accessible for health checks regardless of base path
	s.router.HandleFunc("/health", s.handleHealth).Methods("GET")
	s.router.HandleFunc("/ready", s.handleReadiness).Methods("GET")
	s.router.HandleFunc("/metrics", s.handleMetrics).Methods("GET")

	// Also register health endpoints on the base path for consistency
	if basePath != "" {
		baseRouter.HandleFunc("/health", s.handleHealth).Methods("GET")
		baseRouter.HandleFunc("/ready", s.handleReadiness).Methods("GET")
		baseRouter.HandleFunc("/metrics", s.handleMetrics).Methods("GET")
	}

	// Create a file server for static files
	fs := http.FileServer(http.Dir("./ui/public"))

	// Add a handler for the root of the base path to serve index.html
	baseRouter.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Set security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; object-src 'none'; connect-src 'self' ws: wss:")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

		// Serve the index.html file
		http.ServeFile(w, r, "./ui/public/index.html")
	})

	// Create a handler for static files that strips the base path prefix
	var staticHandler http.Handler
	if basePath != "" {
		// For base path, we need to strip the prefix before serving files
		staticHandler = http.StripPrefix(basePath, fs)
	} else {
		staticHandler = fs
	}

	// Serve static files with security headers
	baseRouter.PathPrefix("/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; object-src 'none'; connect-src 'self' ws: wss:")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// Set caching headers for static assets
		if strings.HasSuffix(r.URL.Path, ".js") ||
			strings.HasSuffix(r.URL.Path, ".css") ||
			strings.HasSuffix(r.URL.Path, ".png") ||
			strings.HasSuffix(r.URL.Path, ".jpg") ||
			strings.HasSuffix(r.URL.Path, ".svg") {
			w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate") // No caching for HTML/other
		}

		// Log the request path for debugging
		s.logger.Debug("Serving static file", map[string]interface{}{
			"path": r.URL.Path,
		})

		// Serve the file
		staticHandler.ServeHTTP(w, r)
	}))
}

// Start initializes HTTP server
func (s *Server) Start() error {
	// Start HTTP server
	port := s.config.General.Port
	if port == 0 {
		port = 8080 // Default port
	}

	s.logger.Info("Starting server", map[string]interface{}{
		"port":                   port,
		"base_path":              s.config.General.BasePath,
		"auth_enabled":           s.config.General.Auth.Enabled,
		"auth_type":              s.config.General.Auth.Type,
		"using_informers":        true,
		"max_connections":        s.maxConnections,
		"max_connections_per_ip": 20,
	})

	// Ensure we clean up resources when the server exits
	defer func() {
		s.stopUpdateListener()
		s.informerCollector.Stop()
	}()

	return http.ListenAndServe(fmt.Sprintf(":%d", port), s.router)
}

// startUpdateListener starts listening for updates from the informer collector
func (s *Server) startUpdateListener() {
	// Get the update channel from the informer collector
	updateCh := s.informerCollector.GetUpdateChannel()

	// Start a goroutine to listen for updates
	go func() {
		for {
			select {
			case <-updateCh:
				// Get the reason for this update
				updateReason := s.informerCollector.GetLastUpdateReason()

				// Only process updates if we have active clients
				if s.activeClients.Load() > 0 {
					s.logger.Info("Received update from informer", map[string]interface{}{
						"active_clients": s.activeClients.Load(),
						"reason":         updateReason,
					})

					// Refresh data and broadcast to clients
					if err := s.refreshData(); err != nil {
						s.logger.Error("Error processing informer update", err, map[string]interface{}{
							"reason": updateReason,
						})
					}
				} else {
					s.logger.Debug("Received update from informer but no active clients", map[string]interface{}{
						"action": "skipping refresh",
						"reason": updateReason,
					})
				}
			case <-s.updateDone:
				return
			}
		}
	}()
}

// stopUpdateListener stops the update listener
func (s *Server) stopUpdateListener() {
	select {
	case <-s.updateDone: // Already closed
		return
	default:
		close(s.updateDone)
	}
}

// handleWebSocket upgrades HTTP connection to WebSocket and manages the connection
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Get client IP address
	ipAddress := getClientIP(r)

	// Check connection limits (if enabled)
	if !s.checkConnectionLimits(ipAddress) {
		// Log connection limit exceeded
		s.logger.SecurityEvent("WebSocket connection limit exceeded", map[string]interface{}{
			"client_ip":          ipAddress,
			"active_clients":     s.activeClients.Load(),
			"connections_per_ip": s.getConnectionsPerIP(ipAddress),
			"max_connections":    s.maxConnections,
		})
		ErrorResponse(w, StandardError{
			Status:  http.StatusTooManyRequests,
			Code:    "connection_limit_exceeded",
			Message: "Too many connections. Please try again later.",
		})
		return
	}

	// Upgrade connection to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("Failed to upgrade to WebSocket", err, map[string]interface{}{
			"client_ip": ipAddress,
			"path":      r.URL.Path,
		})
		return
	}

	// Create new client
	client := &websocketClient{
		conn:        conn,
		server:      s,
		ipAddress:   ipAddress,
		connectTime: time.Now(),
	}

	// Set read/write deadlines
	conn.SetReadDeadline(time.Now().Add(120 * time.Second)) // 2 minute read timeout
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second)) // 10 second write timeout

	// Register client
	s.registerClient(client)
	defer s.unregisterClient(client)

	// Send initial data to the client
	s.sendInitialData(client)

	// Keep connection alive until closed
	for {
		// Read message (for pings, client commands, etc.)
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Error("WebSocket read error", err, map[string]interface{}{
					"client_ip":    client.ipAddress,
					"connect_time": client.connectTime.Format(time.RFC3339),
					"duration":     time.Since(client.connectTime).String(),
				})
			}
			break
		}

		// Reset read deadline after successful read
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))

		// Handle WebSocket protocol-level ping
		if messageType == websocket.PingMessage {
			// Use the mutex to protect the WebSocket write
			client.writeMutex.Lock()
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := conn.WriteMessage(websocket.PongMessage, nil)
			client.writeMutex.Unlock()

			if err != nil {
				break
			}
			continue
		}

		// Handle application-level JSON ping message
		if messageType == websocket.TextMessage && len(message) > 0 {
			// Try to parse as JSON
			var pingMsg struct {
				Type string `json:"type"`
			}

			if err := json.Unmarshal(message, &pingMsg); err == nil && pingMsg.Type == "ping" {
				s.logger.Debug("Received client ping message", map[string]interface{}{
					"client_ip": client.ipAddress,
				})

				// Send pong response
				// Use the mutex to protect the WebSocket write
				client.writeMutex.Lock()
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				pongMsg := struct {
					Type string `json:"type"`
				}{
					Type: "pong",
				}
				err := conn.WriteJSON(pongMsg)
				client.writeMutex.Unlock()

				if err != nil {
					s.logger.Error("Error sending pong response", err, map[string]interface{}{
						"client_ip": client.ipAddress,
					})
					break
				}
			}
		}
	}
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	// Check for X-Forwarded-For header (common when behind proxies/ingress)
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		// X-Forwarded-For can contain multiple IPs, use the first one
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}

	// Check for X-Real-IP header (used by some proxies)
	ip = r.Header.Get("X-Real-IP")
	if ip != "" {
		return ip
	}

	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // Return as-is if we can't split it
	}
	return ip
}

// getConnectionsPerIP returns the number of connections from an IP address
func (s *Server) getConnectionsPerIP(ipAddress string) int {
	s.connsByIPMutex.Lock()
	defer s.connsByIPMutex.Unlock()
	return s.connsByIP[ipAddress]
}

// checkConnectionLimits checks if a new connection is allowed
func (s *Server) checkConnectionLimits(ipAddress string) bool {

	s.connsByIPMutex.Lock()
	defer s.connsByIPMutex.Unlock()

	// Check total connections
	totalConnections := s.activeClients.Load()
	if totalConnections >= int32(s.maxConnections) {
		return false
	}

	// Check connections per IP (max 20 per IP)
	connsFromIP := s.connsByIP[ipAddress]
	if connsFromIP >= 20 {
		return false
	}

	// Increment connection count for this IP
	s.connsByIP[ipAddress] = connsFromIP + 1
	return true
}

// registerClient adds a new WebSocket client
func (s *Server) registerClient(client *websocketClient) {
	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	// Register client
	s.clients[client] = true
	clientCount := s.activeClients.Add(1)
	s.logger.Debug("New WebSocket client connected", map[string]interface{}{
		"client_ip":      client.ipAddress,
		"active_clients": clientCount,
		"connect_time":   client.connectTime.Format(time.RFC3339),
	})

	// If this is the first client, perform an immediate refresh
	if clientCount == 1 {
		// Perform an immediate refresh to ensure fresh data
		go func() {
			s.logger.Debug("Triggering data refresh due to first client connection", map[string]interface{}{
				"client_ip": client.ipAddress,
				"reason":    "First client connected",
			})

			if err := s.refreshData(); err != nil {
				s.logger.Error("Error in immediate refresh on client connect", err, map[string]interface{}{
					"client_ip": client.ipAddress,
					"reason":    "First client connected",
				})
			}
		}()
	}
}

// unregisterClient removes a WebSocket client
func (s *Server) unregisterClient(client *websocketClient) {
	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	// Check if client is registered before deleting
	if _, ok := s.clients[client]; ok {
		// Close connection
		client.conn.Close()

		// Unregister client
		delete(s.clients, client)
		clientCount := s.activeClients.Add(-1)
		s.logger.Debug("WebSocket client disconnected", map[string]interface{}{
			"client_ip":      client.ipAddress,
			"active_clients": clientCount,
			"connect_time":   client.connectTime.Format(time.RFC3339),
			"duration":       time.Since(client.connectTime).String(),
		})

		// Update IP connection tracking
		if client.ipAddress != "" {
			s.connsByIPMutex.Lock()
			s.connsByIP[client.ipAddress]--
			if s.connsByIP[client.ipAddress] <= 0 {
				delete(s.connsByIP, client.ipAddress)
			}
			s.connsByIPMutex.Unlock()
		}

		// Log if no clients left
		if clientCount == 0 {
			s.logger.Info("No clients connected", nil)
		}
	}
}

// sendInitialData sends the current state to a newly connected client
func (s *Server) sendInitialData(client *websocketClient) {
	// Make a copy of the data under a lock to avoid race conditions
	s.mutex.RLock()
	apps := append([]models.Application{}, s.applications...)
	lastUpdated := s.lastRefresh
	s.mutex.RUnlock()

	// Force health recalculation once more on the copy we're about to send
	// log.Println("Sending initial data to client, checking application health status:")
	for i := range apps {
		// Store original health
		originalHealth := apps[i].Health

		// Recalculate health
		apps[i].CalculateHealth()

		// Always log the health status for initial data
		// log.Printf("Initial data: Application %s health status: %s",
		// 	apps[i].Name, apps[i].Health)

		// Check if health changed
		if originalHealth != apps[i].Health {
			log.Printf("WARNING: Application %s health changed from %s to %s during initial data send!",
				apps[i].Name, originalHealth, apps[i].Health)

			// Debug pod statuses for this app
			log.Printf("Application %s pods with issues:", apps[i].Name)
			for _, pod := range apps[i].Pods {
				if pod.Missing || pod.ZeroPods {
					log.Printf(" - %s: Kind=%s, Missing=%v, ZeroPods=%v",
						pod.Name, pod.Kind, pod.Missing, pod.ZeroPods)
				}
			}
		}
	}

	// Create response with the checked applications
	response := struct {
		Applications []models.Application `json:"applications"`
		LastUpdated  time.Time            `json:"lastUpdated"`
	}{
		Applications: apps,
		LastUpdated:  lastUpdated,
	}

	// Set write deadline and send data without holding the lock
	// Use the mutex to protect the WebSocket write
	client.writeMutex.Lock()
	client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := client.conn.WriteJSON(response)
	client.writeMutex.Unlock()

	if err != nil {
		s.logger.Error("Error sending initial data to client", err, map[string]interface{}{
			"client_ip":    client.ipAddress,
			"connect_time": client.connectTime.Format(time.RFC3339),
		})
	}
}

// broadcastToClients sends updated data to all connected clients
func (s *Server) broadcastToClients() {
	// First create a copy of the data to avoid holding locks longer than necessary
	s.mutex.RLock()
	apps := append([]models.Application{}, s.applications...)
	lastUpdated := s.lastRefresh
	s.mutex.RUnlock()

	// Force health recalculation once more on the copy we're about to send
	for i := range apps {
		// Store original health
		originalHealth := apps[i].Health

		// Recalculate health
		apps[i].CalculateHealth()

		// Check if health changed
		if originalHealth != apps[i].Health {
			log.Printf("WARNING: Application %s health changed from %s to %s during broadcast!",
				apps[i].Name, originalHealth, apps[i].Health)

			// Debug pod statuses for this app
			log.Printf("Application %s pods:", apps[i].Name)
			for _, pod := range apps[i].Pods {
				if pod.Missing || pod.ZeroPods {
					log.Printf(" - %s: Kind=%s, Missing=%v, ZeroPods=%v",
						pod.Name, pod.Kind, pod.Missing, pod.ZeroPods)
				}
			}
		}
	}

	// Create response with the checked applications
	responseData := struct {
		Applications []models.Application `json:"applications"`
		LastUpdated  time.Time            `json:"lastUpdated"`
	}{
		Applications: apps,
		LastUpdated:  lastUpdated,
	}

	// Now get the list of clients under a lock
	s.clientsMutex.Lock()
	if len(s.clients) == 0 {
		s.clientsMutex.Unlock()
		return
	}

	// Create a local copy of clients to avoid prolonged lock
	clientsCopy := make([]*websocketClient, 0, len(s.clients))
	for client := range s.clients {
		clientsCopy = append(clientsCopy, client)
	}
	s.clientsMutex.Unlock()

	// Now we can send to all clients without holding the lock
	var failedClients []*websocketClient

	for _, client := range clientsCopy {
		// Use the mutex to protect the WebSocket write
		client.writeMutex.Lock()

		// Set write deadline before sending
		client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err := client.conn.WriteJSON(responseData)

		// Release the mutex
		client.writeMutex.Unlock()

		if err != nil {
			s.logger.Error("Error broadcasting to client", err, map[string]interface{}{
				"client_ip":    client.ipAddress,
				"connect_time": client.connectTime.Format(time.RFC3339),
				"duration":     time.Since(client.connectTime).String(),
			})
			client.conn.Close()
			failedClients = append(failedClients, client)
		}
	}

	// Clean up any failed clients
	if len(failedClients) > 0 {
		s.clientsMutex.Lock()
		for _, client := range failedClients {
			if _, exists := s.clients[client]; exists {
				delete(s.clients, client)
				s.activeClients.Add(-1)

				// Update IP connection tracking
				if client.ipAddress != "" {
					s.connsByIPMutex.Lock()
					s.connsByIP[client.ipAddress]--
					if s.connsByIP[client.ipAddress] <= 0 {
						delete(s.connsByIP, client.ipAddress)
					}
					s.connsByIPMutex.Unlock()
				}
			}
		}
		s.clientsMutex.Unlock()
	}

	// Get the reason for this update if available
	updateReason := s.informerCollector.GetLastUpdateReason()
	if updateReason == "" {
		updateReason = "Manual refresh"
	}

	// Log the broadcast with the reason
	s.logger.Debug("Broadcasted updates to clients", map[string]interface{}{
		"client_count": len(clientsCopy),
		"reason":       updateReason,
	})

	log.Printf("Broadcasted updates to %d clients (reason: %s)", len(clientsCopy), updateReason)
}

// handleGetApplications handles GET /api/applications
func (s *Server) handleGetApplications(w http.ResponseWriter, r *http.Request) {
	// Get applications from cache with a safe copy
	s.mutex.RLock()
	applications := append([]models.Application{}, s.applications...)
	lastUpdated := s.lastRefresh
	s.mutex.RUnlock()

	// Track application access for LRU caching
	for _, app := range applications {
		s.updateCacheAccess(app.Name)
	}

	// Force health recalculation before sending HTTP response
	log.Println("HTTP API: Checking application health status before response:")
	for i := range applications {
		// Store original health
		originalHealth := applications[i].Health

		// Recalculate health
		applications[i].CalculateHealth()

		// Check if health changed
		if originalHealth != applications[i].Health {
			log.Printf("WARNING: HTTP API - Application %s health changed from %s to %s!",
				applications[i].Name, originalHealth, applications[i].Health)

			// Debug pod statuses for this app
			log.Printf("Application %s pods with issues:", applications[i].Name)
			for _, pod := range applications[i].Pods {
				if pod.Missing || pod.ZeroPods {
					log.Printf(" - %s: Kind=%s, Missing=%v, ZeroPods=%v",
						pod.Name, pod.Kind, pod.Missing, pod.ZeroPods)
				}
			}
		}
	}

	// Return JSON response with format matching v1
	w.Header().Set("Content-Type", "application/json")

	// Format response to match v1 API
	response := struct {
		Applications []models.Application `json:"applications"`
		LastUpdated  time.Time            `json:"lastUpdated"`
	}{
		Applications: applications,
		LastUpdated:  lastUpdated,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding applications: %v", err)
		ErrorResponse(w, StandardError{
			Status:  http.StatusInternalServerError,
			Code:    "encoding_error",
			Message: "Failed to encode response",
		})
		return
	}
}

// authMiddleware handles authentication for API endpoints
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth if disabled
		if !s.config.General.Auth.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		authType := strings.ToLower(s.config.General.Auth.Type)

		switch authType {
		case "basic":
			// Basic auth
			username, password, ok := r.BasicAuth()
			if !ok || username != s.config.General.Auth.Username || password != s.config.General.Auth.Password {
				// Log authentication failure
				s.logger.SecurityEvent("Authentication failure", map[string]interface{}{
					"client_ip": getClientIP(r),
					"method":    r.Method,
					"path":      r.URL.Path,
					"auth_type": "basic",
					"username":  username,
				})
				w.Header().Set("WWW-Authenticate", `Basic realm="Kubernetes Pod Monitor"`)
				ErrorResponse(w, StandardError{
					Status:  http.StatusUnauthorized,
					Code:    "unauthorized",
					Message: "Invalid credentials",
				})
				return
			}

		case "token", "api-key", "apikey":
			// Token auth (via header or query param)
			token := r.Header.Get("Authorization")
			if token == "" {
				// Check query parameter if header is missing
				token = r.URL.Query().Get("api_key")
			} else {
				// Remove "Bearer " prefix if present
				if strings.HasPrefix(token, "Bearer ") {
					token = token[7:]
				}
			}

			if token != s.config.General.Auth.APIKey {
				// Log authentication failure
				s.logger.SecurityEvent("Authentication failure", map[string]interface{}{
					"client_ip": getClientIP(r),
					"method":    r.Method,
					"path":      r.URL.Path,
					"auth_type": "token",
				})
				ErrorResponse(w, StandardError{
					Status:  http.StatusUnauthorized,
					Code:    "invalid_token",
					Message: "Invalid or missing API token",
				})
				return
			}

		default:
			// Unknown auth type, log a warning and continue
			log.Printf("Warning: Unknown auth type configured: %s", authType)
		}

		// Authentication successful, continue to the actual handler
		next.ServeHTTP(w, r)
	})
}

// Note: handleRefresh endpoint has been removed as we now use real-time updates via WebSockets

// updateCacheAccess records an access to an item for LRU tracking
func (s *Server) updateCacheAccess(key string) {
	s.accessMutex.Lock()
	defer s.accessMutex.Unlock()

	// Find and remove existing entry
	for i, k := range s.cacheAccessOrder {
		if k == key {
			s.cacheAccessOrder = append(s.cacheAccessOrder[:i], s.cacheAccessOrder[i+1:]...)
			break
		}
	}

	// Add to end (most recently used)
	s.cacheAccessOrder = append(s.cacheAccessOrder, key)

	// Print cache size for debugging
	// log.Printf("\nCache size: %d, Key: %s\n", len(s.cacheAccessOrder), key)

	// Perform LRU eviction if needed
	if len(s.cacheAccessOrder) > s.maxCacheSize {
		// Remove oldest entries directly (keep within lock to avoid race)
		excess := len(s.cacheAccessOrder) - s.maxCacheSize
		if excess > 0 {
			s.cacheAccessOrder = s.cacheAccessOrder[excess:]
			log.Printf("Evicted %d oldest entries from cache", excess)
		}
	}
}

// handleGetApplicationByName handles GET /api/applications/{name}
func (s *Server) handleGetApplicationByName(w http.ResponseWriter, r *http.Request) {
	// Get application name from URL
	vars := mux.Vars(r)
	name := vars["name"]

	// Validate input
	if name == "" {
		ErrorResponse(w, StandardError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_parameter",
			Message: "Application name is required",
		})
		return
	}

	// Check for invalid characters
	if strings.ContainsAny(name, "<>\"'%;()&+") {
		ErrorResponse(w, StandardError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_parameter",
			Message: "Application name contains invalid characters",
		})
		return
	}

	// Get applications from cache with a safe copy
	s.mutex.RLock()
	applications := append([]models.Application{}, s.applications...)
	s.mutex.RUnlock()

	// Find the requested application
	for _, app := range applications {
		if app.Name == name {
			// Record access for LRU caching
			s.updateCacheAccess(name)

			// Return JSON response
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(app); err != nil {
				log.Printf("Error encoding application: %v", err)
				ErrorResponse(w, StandardError{
					Status:  http.StatusInternalServerError,
					Code:    "encoding_error",
					Message: "Failed to encode response",
				})
				return
			}
			return
		}
	}

	// Application not found
	ErrorResponse(w, StandardError{
		Status:  http.StatusNotFound,
		Code:    "not_found",
		Message: "Application not found",
	})
}

// handleGetNamespaces handles GET /api/namespaces
func (s *Server) handleGetNamespaces(w http.ResponseWriter, r *http.Request) {

	// Get applications from cache
	s.mutex.RLock()
	applications := s.applications
	s.mutex.RUnlock()

	// Create a map to deduplicate namespaces
	namespaceMap := make(map[string]bool)
	for _, app := range applications {
		// Add all namespaces from each application
		for _, ns := range app.Namespaces {
			namespaceMap[ns] = true
		}
	}

	// Convert map to slice
	namespaces := make([]string, 0, len(namespaceMap))
	for ns := range namespaceMap {
		namespaces = append(namespaces, ns)
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(namespaces); err != nil {
		log.Printf("Error encoding namespaces: %v", err)
		ErrorResponse(w, StandardError{
			Status:  http.StatusInternalServerError,
			Code:    "encoding_error",
			Message: "Failed to encode response",
		})
		return
	}
}

// handleGetConfig handles GET /api/config
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	// Only support GET method
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Create a client-facing config with only necessary information
	clientConfig := struct {
		DashboardName string `json:"dashboardName"`
		Version       string `json:"version"`
	}{
		DashboardName: s.config.General.Name,
		Version:       version.Version,
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(clientConfig); err != nil {
		log.Printf("Error encoding config: %v", err)
		ErrorResponse(w, StandardError{
			Status:  http.StatusInternalServerError,
			Code:    "encoding_error",
			Message: "Failed to encode response",
		})
		return
	}
}

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Simple health check response
	health := struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Uptime  string `json:"uptime"`
	}{
		Status:  "ok",
		Version: version.Version, // Using the version constant
		Uptime:  time.Since(s.startTime).String(),
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(health); err != nil {
		log.Printf("Error encoding health: %v", err)
	}
}

// handleReadiness checks if the service is ready to handle requests
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	// Set header
	w.Header().Set("Content-Type", "application/json")

	// Check if we have successfully refreshed data at least once
	ready := !s.lastRefresh.IsZero()

	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not_ready",
			"reason": "initial_data_load_incomplete",
		})
		return
	}

	// Check if we can reach the Kubernetes API
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := s.collector.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not_ready",
			"reason": "kubernetes_api_unavailable",
			"error":  err.Error(),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ready",
	})
}

// handleMetrics returns basic metrics about the server
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Get data to calculate metrics with a safe copy
	s.mutex.RLock()
	applications := append([]models.Application{}, s.applications...)
	lastRefresh := s.lastRefresh
	appCount := len(applications)
	s.mutex.RUnlock()

	// Count total pods using our local copy
	var podCount int
	for _, app := range applications {
		podCount += len(app.Pods)
	}

	metrics := struct {
		ApplicationCount  int           `json:"applicationCount"`
		TotalPodCount     int           `json:"totalPodCount"`
		LastRefreshTime   time.Time     `json:"lastRefreshTime"`
		Uptime            time.Duration `json:"uptime"`
		ActiveConnections int32         `json:"activeConnections"`
		CPUUsage          string        `json:"cpuUsage"`
		MemoryUsage       string        `json:"memoryUsage"`
	}{
		ApplicationCount:  appCount,
		TotalPodCount:     podCount,
		LastRefreshTime:   lastRefresh,
		Uptime:            time.Since(s.startTime),
		ActiveConnections: s.activeClients.Load(),
		CPUUsage:          "n/a", // Would need runtime metrics to provide real data
		MemoryUsage:       "n/a", // Would need runtime metrics to provide real data
	}

	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		log.Printf("Error encoding metrics response: %v", err)
	}
}
