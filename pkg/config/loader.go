package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"kpods-monitor/pkg/models"
)

// ConfigLoader handles loading and validating configuration
type ConfigLoader struct {
	configPath string
}

// NewConfigLoader creates a new config loader
func NewConfigLoader(configPath string) *ConfigLoader {
	return &ConfigLoader{
		configPath: configPath,
	}
}

// Load reads and parses the configuration file
func (cl *ConfigLoader) Load() (*models.Config, error) {
	// Expand tilde if present in path
	if strings.HasPrefix(cl.configPath, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to expand home directory: %w", err)
		}
		cl.configPath = filepath.Join(homeDir, cl.configPath[2:])
	}

	// Read file content
	data, err := os.ReadFile(cl.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse YAML
	var config models.Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate configuration
	if err := cl.validate(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// validate performs Application validation checks on the loaded configuration
func (cl *ConfigLoader) validate(config *models.Config) error {
	// Basic validation
	if config.General.Name == "" {
		return fmt.Errorf("general.name is required")
	}

	if config.General.Port <= 0 || config.General.Port > 65535 {
		return fmt.Errorf("general.port must be between 1 and 65535")
	}

	// Validate cluster configuration
	if !config.Cluster.InCluster && config.Cluster.KubeConfigPath == "" {
		return fmt.Errorf("when not using in-cluster config, kubeConfigPath is required")
	}

	// Validate applications
	if len(config.Applications) == 0 {
		return fmt.Errorf("at least one application must be configured")
	}

	for i, app := range config.Applications {
		if app.Name == "" {
			return fmt.Errorf("applications[%d].name is required", i)
		}

		// Validate selector map
		if len(app.Selector) == 0 {
			return fmt.Errorf("applications[%d].selector is required and must contain at least one namespace", i)
		}

		// Check each namespace selector
		hasValidSelector := false
		for namespace, selector := range app.Selector {
			if namespace == "" {
				return fmt.Errorf("applications[%d].selector has an empty namespace key", i)
			}

			// Ensure there's at least one selector for this namespace
			hasNamespaceSelector := len(selector.Labels) > 0 ||
				len(selector.Annotations) > 0 ||
				len(selector.Deployments) > 0 ||
				len(selector.StatefulSets) > 0 ||
				len(selector.DaemonSets) > 0 ||
				len(selector.Jobs) > 0 ||
				len(selector.CronJobs) > 0

			if hasNamespaceSelector {
				hasValidSelector = true
			}
		}

		if !hasValidSelector {
			return fmt.Errorf("applications[%s].selector must specify at least one selection method across all namespaces", app.Name)
		}
	}

	return nil
}
