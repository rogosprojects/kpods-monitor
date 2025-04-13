package models

// Config represents the main application configuration
type Config struct {
	// General settings for the dashboard application
	General GeneralConfig `json:"general" yaml:"general"`

	// Kubernetes cluster connection settings
	Cluster ClusterConfig `json:"cluster" yaml:"cluster"`

	// Applications to monitor
	Applications []ApplicationConfig `json:"applications" yaml:"applications"`
}

// GeneralConfig contains general dashboard settings
type GeneralConfig struct {
	// Name of the dashboard instance
	Name string `json:"name" yaml:"name"`

	// Dashboard refresh interval in seconds
	RefreshInterval int `json:"refreshInterval" yaml:"refreshInterval"`

	// Port number for the dashboard server
	Port int `json:"port" yaml:"port"`

	// Enable or disable debug logging
	Debug bool `json:"debug" yaml:"debug"`

	// Authentication configuration
	Auth AuthConfig `json:"auth" yaml:"auth"`
}

// AuthConfig contains authentication settings
type AuthConfig struct {
	// Enable or disable authentication
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Authentication type (basic, token, none)
	Type string `json:"type" yaml:"type"`

	// API key for token authentication
	APIKey string `json:"apiKey" yaml:"apiKey"`

	// Basic auth username
	Username string `json:"username" yaml:"username"`

	// Basic auth password
	Password string `json:"password" yaml:"password"`
}

// ClusterConfig contains Kubernetes cluster connection settings
type ClusterConfig struct {
	// Use in-cluster config when running inside Kubernetes
	InCluster bool `json:"inCluster" yaml:"inCluster"`

	// Path to kubeconfig file
	KubeConfigPath string `json:"kubeConfigPath" yaml:"kubeConfigPath"`
}

// ApplicationConfig defines a logical application composed of related workloads
type ApplicationConfig struct {
	// Name of the application
	Name string `json:"name" yaml:"name"`

	// Description of the application
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Display order in the UI (lower values appear first)
	Order int `json:"order,omitempty" yaml:"order,omitempty"`

	// Selector defines which workloads to include in each namespace
	// It's a map of namespace name to workload selector
	Selector map[string]WorkloadSelector `json:"selector" yaml:"selector"`
}

// WorkloadSelector defines how to select Kubernetes workloads for an application
type WorkloadSelector struct {
	// Match workloads by labels
	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`

	// Match workloads by annotations
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`

	// Include deployments with these names
	Deployments []string `json:"deployments,omitempty" yaml:"deployments,omitempty"`

	// Include statefulsets with these names
	StatefulSets []string `json:"statefulSets,omitempty" yaml:"statefulsets,omitempty"`

	// Include daemonsets with these names
	DaemonSets []string `json:"daemonSets,omitempty" yaml:"daemonSets,omitempty"`

	// Include jobs with these names
	Jobs []string `json:"jobs,omitempty" yaml:"jobs,omitempty"`

	// Include cronjobs with these names
	CronJobs []string `json:"cronJobs,omitempty" yaml:"cronJobs,omitempty"`
}
