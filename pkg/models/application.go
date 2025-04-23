package models

import (
	"kpods-monitor/pkg/logger"
	"time"
)

// PodStatus represents the current status of a Kubernetes pod
type PodStatus string

const (
	PodStatusRunning          PodStatus = "Running"
	PodStatusPending          PodStatus = "Pending"
	PodStatusCrashLoopBackOff PodStatus = "CrashLoopBackOff"
	PodStatusError            PodStatus = "Error"
	PodStatusCompleted        PodStatus = "Completed"
	PodStatusTerminating      PodStatus = "Terminating"
)

// HealthStatus represents the overall health of an application
type HealthStatus string

const (
	HealthStatusHealthy  HealthStatus = "Healthy"
	HealthStatusWarning  HealthStatus = "Warning"
	HealthStatusCritical HealthStatus = "Critical"
)

// TrendDirection represents the direction of a metric trend
type TrendDirection string

const (
	TrendUp     TrendDirection = "up"
	TrendDown   TrendDirection = "down"
	TrendStatic TrendDirection = "static"
)

// ContainerStatus represents the status of a container in a pod
type ContainerStatus struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Status  string `json:"status"` // running, waiting, terminated
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Pod represents a Kubernetes pod in our application
type Pod struct {
	Name              string            `json:"name"`
	Status            PodStatus         `json:"status"`
	StartTime         time.Time         `json:"startTime"`
	Age               string            `json:"age"` // Human readable age
	Restarts          int               `json:"restarts"`
	CPU               string            `json:"cpu"`         // CPU usage
	CPUValue          float64           `json:"-"`           // CPU usage in millicores for internal use
	CPUTrend          TrendDirection    `json:"cpuTrend"`    // Direction of CPU usage trend
	Memory            string            `json:"memory"`      // Memory usage
	MemoryValue       float64           `json:"-"`           // Memory usage in bytes for internal use
	MemoryTrend       TrendDirection    `json:"memoryTrend"` // Direction of memory usage trend
	Kind              string            `json:"kind"`        // Deployment, StatefulSet, DaemonSet, etc.
	Namespace         string            `json:"namespace"`
	Missing           bool              `json:"missing"`                     // True if workload is in config but not found in cluster
	ZeroPods          bool              `json:"zeroPods"`                    // True if workload exists but has 0 running pods
	OwnerName         string            `json:"ownerName,omitempty"`         // Name of the owner workload (if applicable)
	ContainerStatuses []ContainerStatus `json:"containerStatuses,omitempty"` // Status of each container in the pod
	TotalContainers   int               `json:"totalContainers"`             // Total number of containers in the pod
	ReadyContainers   int               `json:"readyContainers"`             // Number of ready containers in the pod
}

// Application represents a group of related pods that form a logical application
type Application struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"` // Optional description of the application
	Namespaces  []string     `json:"namespaces"`            // All namespaces this application spans
	Health      HealthStatus `json:"health"`
	Pods        []Pod        `json:"pods"`
	Order       int          `json:"order"` // Display order in UI (lower values appear first)
}

// CalculateHealth determines the overall health status of an application based on its pods
func (app *Application) CalculateHealth() HealthStatus {
	// Health calculation logic
	// - If any pod is in CrashLoopBackOff or Error, the application is Critical
	// - If any pod is in Pending or Terminating, the application is Warning
	// - If any pod has more than 3 restarts, the application is Warning
	// - If any pod is Missing (defined in config but not found), the application is Warning
	// - If any workload has 0 pods running (ZeroPods=true), the application is Warning
	// - Otherwise, the application is Healthy

	hasCritical := false
	hasWarning := false
	hasMissing := false
	hasZeroPods := false
	missingPods := 0
	zeroPodWorkloads := 0

	for _, pod := range app.Pods {
		switch pod.Status {
		case PodStatusCrashLoopBackOff, PodStatusError:
			hasCritical = true
		case PodStatusPending, PodStatusTerminating:
			hasWarning = true
		}

		// Consider high restart counts as a warning
		if pod.Restarts > 10 {
			hasWarning = true
		}

		// Consider missing pods (workloads not found in cluster) as a warning
		if pod.Missing {
			hasMissing = true
			missingPods++
		}

		// Consider workloads with zero pods as a warning
		if pod.ZeroPods {
			hasZeroPods = true
			zeroPodWorkloads++
		}
	}

	logger.DefaultLogger.Debug("Application health calculation", map[string]interface{}{
		"app_name":           app.Name,
		"critical":           hasCritical,
		"warning":            hasWarning,
		"missing":            hasMissing,
		"missing_pod_count":  missingPods,
		"zero_pod_workloads": hasZeroPods,
		"zero_pod_count":     zeroPodWorkloads,
		"total_pod_count":    len(app.Pods),
	})

	if hasCritical {
		logger.DefaultLogger.Error("Application has critical status", nil, map[string]interface{}{
			"app_name": app.Name,
		})

		app.Health = HealthStatusCritical
		return HealthStatusCritical
	}

	if hasWarning || hasMissing || hasZeroPods {

		logger.DefaultLogger.Warn("Application has warning status", map[string]interface{}{
			"app_name": app.Name,
		})

		app.Health = HealthStatusWarning
		return HealthStatusWarning
	}

	app.Health = HealthStatusHealthy
	return HealthStatusHealthy
}
