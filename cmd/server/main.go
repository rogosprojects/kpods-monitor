package main

import (
	"flag"
	"fmt"
	"kpods-monitor/pkg/api"
	"kpods-monitor/pkg/config"
	"kpods-monitor/pkg/log"
	"kpods-monitor/pkg/version"
	"os"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	// Show version if requested
	if *showVersion {
		fmt.Printf("Kubernetes Pod Monitor %s\n", version.Version)
		os.Exit(0)
	}

	// Load configuration
	log.Printf("Loading configuration from %s", *configPath)
	configLoader := config.NewConfigLoader(*configPath)
	cfg, err := configLoader.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	// Initialize Kubernetes collector
	log.Println("Initializing Kubernetes data collector")
	collector, err := api.NewCollector(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize collector: %v", err)
	}

	// Test collector by retrieving applications
	log.Println("Testing connection to Kubernetes...")
	apps, err := collector.CollectApplications()
	if err != nil {
		log.Fatalf("Failed to collect applications: %v", err)
	}
	log.Printf("Successfully connected to Kubernetes and found %d applications", len(apps))

	// Enable debug logging if configured
	if cfg.General.Debug {
		log.Println("Debug logging enabled")
	}

	// Initialize and start the server
	log.Println("Initializing server")
	server, err := api.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}

	log.Printf("Starting HTTP server on port %d", cfg.General.Port)
	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
