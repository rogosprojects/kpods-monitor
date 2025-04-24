package api

import (
	"context"
	"fmt"
	"kpods-monitor/pkg/logger"
	"kpods-monitor/pkg/models"
	"math"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// MetricsCollector periodically fetches metrics for all pods in a batch
// and maintains a cache of metrics data for efficient access
type MetricsCollector struct {
	metricsClient  *metricsv.Clientset
	metricsEnabled bool
	logger         *logger.Logger
	config         *models.Config // Reference to the application config

	// Cache of pod metrics
	metricsCache      map[string]*PodMetrics
	metricsCacheMutex sync.RWMutex

	// Channel for notifying about significant metrics updates
	updateCh chan struct{}

	// Control channels
	stopCh chan struct{}
	doneCh chan struct{}

	// Configuration
	collectInterval   time.Duration
	significantChange float64 // Percentage change that triggers an update notification
	numSamples        int     // Number of samples to keep for trend calculation
	cpuThreshold      float64 // Threshold for CPU trend calculation
	memoryThreshold   float64 // Threshold for memory trend calculation

	// Last update reason for debugging
	lastUpdateReason string
	updateReasonLock sync.RWMutex

	// Track which namespaces and workloads we care about
	watchedNamespaces map[string]bool
	watchedWorkloads  map[string]map[string]bool   // Map of namespace to map of workload name to bool
	workloadKinds     map[string]map[string]string // Map of namespace to map of workload name to kind (Deployment, StatefulSet, DaemonSet)
}

// PodMetrics stores metrics data for a single pod
type PodMetrics struct {
	CPU         string                // Formatted CPU usage (e.g., "150m")
	Memory      string                // Formatted memory usage (e.g., "256MiB")
	CPUValue    float64               // Raw CPU value in millicores
	MemoryValue float64               // Raw memory value in bytes
	CPUTrend    models.TrendDirection // Whether CPU usage is increasing, decreasing, or stable
	MemoryTrend models.TrendDirection // Whether memory usage is increasing, decreasing, or stable
	LastUpdated time.Time             // When this metrics data was last updated

	// Historical data for trend calculation
	CPUHistory    []float64   // Historical CPU values
	MemoryHistory []float64   // Historical memory values
	Timestamps    []time.Time // Timestamps for the historical data
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(metricsClient *metricsv.Clientset, metricsEnabled bool, config *models.Config) *MetricsCollector {
	mc := &MetricsCollector{
		metricsClient:     metricsClient,
		metricsEnabled:    metricsEnabled,
		logger:            logger.DefaultLogger,
		config:            config,
		metricsCache:      make(map[string]*PodMetrics),
		updateCh:          make(chan struct{}, 1), // Buffered channel to prevent blocking
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
		collectInterval:   30 * time.Second, // Default to 30 seconds
		significantChange: 0.20,             // 20% change triggers an update notification
		numSamples:        3,                // Keep 3 samples for trend calculation
		cpuThreshold:      0.30,             // 30% threshold for CPU trend
		memoryThreshold:   0.10,             // 10% threshold for memory trend
		watchedNamespaces: make(map[string]bool),
		watchedWorkloads:  make(map[string]map[string]bool),
		workloadKinds:     make(map[string]map[string]string),
	}

	// Extract namespaces and workloads from the config
	if config != nil {
		for _, appConfig := range config.Applications {
			for namespace, selector := range appConfig.Selector {
				// Track the namespace
				mc.watchedNamespaces[namespace] = true

				// Initialize maps for this namespace if needed
				if _, exists := mc.watchedWorkloads[namespace]; !exists {
					mc.watchedWorkloads[namespace] = make(map[string]bool)
					mc.workloadKinds[namespace] = make(map[string]string)
				}

				// Track deployments
				for _, name := range selector.Deployments {
					mc.watchedWorkloads[namespace][name] = true
					mc.workloadKinds[namespace][name] = "Deployment"
				}

				// Track statefulsets
				for _, name := range selector.StatefulSets {
					mc.watchedWorkloads[namespace][name] = true
					mc.workloadKinds[namespace][name] = "StatefulSet"
				}

				// Track daemonsets
				for _, name := range selector.DaemonSets {
					mc.watchedWorkloads[namespace][name] = true
					mc.workloadKinds[namespace][name] = "DaemonSet"
				}
			}
		}

		// Log the namespaces and workload counts we're watching
		var namespaces []string
		var totalWorkloads int
		for namespace, workloads := range mc.watchedWorkloads {
			namespaces = append(namespaces, namespace)
			totalWorkloads += len(workloads)
		}
		mc.logger.Info("Setting up metrics collection", map[string]interface{}{
			"namespaces":      namespaces,
			"total_workloads": totalWorkloads,
		})
	}

	return mc
}

// Start begins the metrics collection loop
func (mc *MetricsCollector) Start() {
	if !mc.metricsEnabled || mc.metricsClient == nil {
		mc.logger.Warn("Metrics collection is disabled", nil)
		return
	}

	mc.logger.Info("Starting metrics collector", map[string]interface{}{
		"collect_interval": mc.collectInterval.String(),
	})

	// Start the collection loop in a goroutine
	go mc.collectLoop()
}

// Stop stops the metrics collection loop
func (mc *MetricsCollector) Stop() {
	select {
	case <-mc.stopCh:
		// Already stopped
		return
	default:
		close(mc.stopCh)
		<-mc.doneCh // Wait for the collection loop to finish
		mc.logger.Info("Metrics collector stopped", nil)
	}
}

// GetUpdateChannel returns the channel that signals when significant metrics updates occur
func (mc *MetricsCollector) GetUpdateChannel() <-chan struct{} {
	return mc.updateCh
}

// GetLastUpdateReason returns the reason for the last metrics update
func (mc *MetricsCollector) GetLastUpdateReason() string {
	mc.updateReasonLock.RLock()
	defer mc.updateReasonLock.RUnlock()
	return mc.lastUpdateReason
}

// isPodWatched checks if a pod belongs to a watched workload
func (mc *MetricsCollector) isPodWatched(podMetrics *metricsv1beta1.PodMetrics) bool {
	namespace := podMetrics.Namespace

	// Check if we're watching this namespace
	if !mc.watchedNamespaces[namespace] {
		return false
	}

	// If we don't have any specific workloads for this namespace, watch all pods in the namespace
	if len(mc.watchedWorkloads[namespace]) == 0 {
		return true
	}

	// Extract owner references from the pod name
	// This is a heuristic approach since we don't have the full pod object
	// For StatefulSets, the pod name format is <statefulset-name>-<ordinal>
	// For Deployments/ReplicaSets, the pod name format is <deployment-name>-<hash>-<random>

	podName := podMetrics.Name

	// First, check for exact match (unlikely but possible)
	if mc.watchedWorkloads[namespace][podName] {
		return true
	}

	// Check for StatefulSet pattern (name-ordinal)
	// The pattern is: <statefulset-name>-<ordinal>
	// For StatefulSets, we need to be careful with names that contain dashes

	// First, find the last dash in the pod name
	lastDashIndex := strings.LastIndex(podName, "-")
	if lastDashIndex != -1 && lastDashIndex < len(podName)-1 {
		// Check if everything after the last dash is a number (StatefulSet ordinal)
		ordinalPart := podName[lastDashIndex+1:]
		isOrdinal := true
		for _, c := range ordinalPart {
			if c < '0' || c > '9' {
				isOrdinal = false
				break
			}
		}

		if isOrdinal {
			// This looks like a StatefulSet pod
			possibleOwner := podName[:lastDashIndex]
			if mc.watchedWorkloads[namespace][possibleOwner] {
				mc.logger.Debug("Pod matched to StatefulSet", map[string]interface{}{
					"pod_name":    podName,
					"statefulset": possibleOwner,
					"ordinal":     ordinalPart,
				})
				return true
			}
		}
	}

	// Check for Deployment/ReplicaSet pattern
	// The pattern is: <deployment-name>-<replicaset-hash>-<random-string>
	for workloadName := range mc.watchedWorkloads[namespace] {
		// First, check if the pod name starts with the workload name followed by a dash
		if !strings.HasPrefix(podName, workloadName+"-") {
			continue
		}

		// Now, check if what follows is a valid ReplicaSet pattern
		remainder := podName[len(workloadName)+1:]

		// Look for another dash that would separate the hash from the random string
		dashIndex := strings.Index(remainder, "-")
		if dashIndex == -1 {
			// No second dash found, this might be a direct controller (unlikely)
			// Log this case for debugging
			mc.logger.Debug("Pod name matches workload prefix but doesn't follow ReplicaSet pattern", map[string]interface{}{
				"pod_name":      podName,
				"workload_name": workloadName,
				"remainder":     remainder,
			})
			continue
		}

		// Check if the hash part is the right length (typically 8-10 characters)
		hashPart := remainder[:dashIndex]
		if len(hashPart) >= 8 && len(hashPart) <= 10 {
			// This looks like a valid ReplicaSet hash pattern
			mc.logger.Debug("Pod matched to workload using improved algorithm", map[string]interface{}{
				"pod_name":      podName,
				"workload_name": workloadName,
				"hash_part":     hashPart,
			})
			return true
		}
	}

	// No match found
	return false
}

// SetCollectInterval sets the interval between metrics collections
func (mc *MetricsCollector) SetCollectInterval(interval time.Duration) {
	if interval < 15*time.Second {
		interval = 15 * time.Second // Minimum 15 seconds to avoid excessive API calls
	}
	mc.collectInterval = interval
	mc.logger.Info("Updated metrics collection interval", map[string]interface{}{
		"interval": interval.String(),
	})
}

// collectLoop periodically collects metrics for all pods
func (mc *MetricsCollector) collectLoop() {
	defer close(mc.doneCh)

	ticker := time.NewTicker(mc.collectInterval)
	defer ticker.Stop()

	// Do an initial collection
	mc.collectAllMetrics()

	for {
		select {
		case <-ticker.C:
			mc.collectAllMetrics()
		case <-mc.stopCh:
			return
		}
	}
}

// collectAllMetrics fetches metrics for pods in the watched namespaces
func (mc *MetricsCollector) collectAllMetrics() {
	if !mc.metricsEnabled || mc.metricsClient == nil {
		return
	}

	// If no namespaces are configured, don't collect any metrics
	if len(mc.watchedNamespaces) == 0 {
		mc.logger.Debug("No namespaces configured for metrics collection", nil)
		return
	}

	mc.logger.Debug("Collecting metrics for pods in watched namespaces", map[string]interface{}{
		"namespace_count": len(mc.watchedNamespaces),
	})

	// Use a timeout context to prevent hanging
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a list to hold all pod metrics
	var allPodMetrics []metav1.Object

	// Get metrics for each namespace separately
	for namespace := range mc.watchedNamespaces {
		mc.logger.Debug("Collecting metrics for namespace", map[string]interface{}{
			"namespace": namespace,
		})

		podMetricsList, err := mc.metricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			mc.logger.Error("Failed to collect pod metrics for namespace", err, map[string]interface{}{
				"namespace": namespace,
			})
			continue
		}

		// Add the pod metrics to our list
		for i := range podMetricsList.Items {
			allPodMetrics = append(allPodMetrics, &podMetricsList.Items[i])
		}
	}

	// Track significant changes to determine if we need to notify listeners
	significantChanges := false
	newPods := 0
	updatedPods := 0

	// Lock for writing to the cache
	mc.metricsCacheMutex.Lock()
	defer mc.metricsCacheMutex.Unlock()

	// Process each pod's metrics
	for _, obj := range allPodMetrics {
		// Get the pod metrics
		podMetricsObj, ok := obj.(*metricsv1beta1.PodMetrics)
		if !ok {
			mc.logger.Debug("Failed to convert object to PodMetrics", nil)
			continue
		}

		// Skip pods that don't belong to watched workloads
		if !mc.isPodWatched(podMetricsObj) {
			mc.logger.Debug("Skipping pod metrics - not part of watched workloads", map[string]interface{}{
				"pod": fmt.Sprintf("%s/%s", podMetricsObj.Namespace, podMetricsObj.Name),
			})
			continue
		}

		// Calculate total CPU and memory usage across all containers
		var cpuUsage int64
		var memoryUsage int64

		for _, container := range podMetricsObj.Containers {
			// CPU is in "n" format, typically in nanocores (n)
			cpu := container.Usage.Cpu().MilliValue()
			cpuUsage += cpu

			// Memory is in bytes
			memory := container.Usage.Memory().Value()
			memoryUsage += memory
		}

		// Create a unique key for this pod
		podKey := fmt.Sprintf("%s/%s", podMetricsObj.Namespace, podMetricsObj.Name)

		// Check if we already have metrics for this pod
		existingMetrics, exists := mc.metricsCache[podKey]
		if !exists {
			// First time seeing this pod
			newPods++
			significantChanges = true

			// Create new metrics entry
			mc.metricsCache[podKey] = &PodMetrics{
				CPU:           fmt.Sprintf("%dm", cpuUsage),
				Memory:        formatMemoryBytes(memoryUsage),
				CPUValue:      float64(cpuUsage),
				MemoryValue:   float64(memoryUsage),
				CPUTrend:      models.TrendStatic,
				MemoryTrend:   models.TrendStatic,
				LastUpdated:   time.Now(),
				CPUHistory:    []float64{float64(cpuUsage)},
				MemoryHistory: []float64{float64(memoryUsage)},
				Timestamps:    []time.Time{time.Now()},
			}
		} else {
			// Update existing metrics
			updatedPods++

			// Check for significant changes
			if exists && len(existingMetrics.CPUHistory) > 0 {
				lastCPU := existingMetrics.CPUHistory[len(existingMetrics.CPUHistory)-1]
				lastMemory := existingMetrics.MemoryHistory[len(existingMetrics.MemoryHistory)-1]

				// Calculate percentage changes
				cpuChange := math.Abs(float64(cpuUsage)-lastCPU) / math.Max(lastCPU, 1.0)
				memoryChange := math.Abs(float64(memoryUsage)-lastMemory) / math.Max(lastMemory, 1.0)

				// Check if either change is significant
				if cpuChange > mc.significantChange || memoryChange > mc.significantChange {
					significantChanges = true
				}
			}

			// Update the metrics
			existingMetrics.CPU = fmt.Sprintf("%dm", cpuUsage)
			existingMetrics.Memory = formatMemoryBytes(memoryUsage)
			existingMetrics.CPUValue = float64(cpuUsage)
			existingMetrics.MemoryValue = float64(memoryUsage)
			existingMetrics.LastUpdated = time.Now()

			// Add to history
			existingMetrics.CPUHistory = append(existingMetrics.CPUHistory, float64(cpuUsage))
			existingMetrics.MemoryHistory = append(existingMetrics.MemoryHistory, float64(memoryUsage))
			existingMetrics.Timestamps = append(existingMetrics.Timestamps, time.Now())

			// Trim history if needed
			if len(existingMetrics.CPUHistory) > mc.numSamples {
				existingMetrics.CPUHistory = existingMetrics.CPUHistory[len(existingMetrics.CPUHistory)-mc.numSamples:]
				existingMetrics.MemoryHistory = existingMetrics.MemoryHistory[len(existingMetrics.MemoryHistory)-mc.numSamples:]
				existingMetrics.Timestamps = existingMetrics.Timestamps[len(existingMetrics.Timestamps)-mc.numSamples:]
			}

			// Calculate trends
			existingMetrics.CPUTrend = mc.calculateTrend(existingMetrics.CPUHistory, mc.cpuThreshold)
			existingMetrics.MemoryTrend = mc.calculateTrend(existingMetrics.MemoryHistory, mc.memoryThreshold)
		}
	}

	// Clean up old entries (pods that no longer exist)
	currentPods := make(map[string]bool)
	for _, obj := range allPodMetrics {
		podMetricsObj, ok := obj.(*metricsv1beta1.PodMetrics)
		if !ok {
			continue
		}
		podKey := fmt.Sprintf("%s/%s", podMetricsObj.Namespace, podMetricsObj.Name)
		currentPods[podKey] = true
	}

	removedPods := 0
	for podKey := range mc.metricsCache {
		if !currentPods[podKey] {
			delete(mc.metricsCache, podKey)
			removedPods++
			significantChanges = true
		}
	}

	mc.logger.Debug("Metrics collection completed", map[string]interface{}{
		"new_pods":     newPods,
		"updated_pods": updatedPods,
		"removed_pods": removedPods,
		"total_pods":   len(mc.metricsCache),
	})

	// Notify listeners if there were significant changes
	if significantChanges {
		// Find which pod had the most significant change
		var significantPod string
		var maxChange float64

		for podKey, metrics := range mc.metricsCache {
			if len(metrics.CPUHistory) > 1 {
				lastCPU := metrics.CPUHistory[len(metrics.CPUHistory)-2]
				currentCPU := metrics.CPUHistory[len(metrics.CPUHistory)-1]

				lastMemory := metrics.MemoryHistory[len(metrics.MemoryHistory)-2]
				currentMemory := metrics.MemoryHistory[len(metrics.MemoryHistory)-1]

				// Calculate percentage changes
				cpuChange := math.Abs(currentCPU-lastCPU) / math.Max(lastCPU, 1.0)
				memoryChange := math.Abs(currentMemory-lastMemory) / math.Max(lastMemory, 1.0)

				// Use the larger of the two changes
				change := math.Max(cpuChange, memoryChange)

				if change > maxChange {
					maxChange = change
					significantPod = podKey
				}
			}
		}

		// If we found a significant pod, include it in the update reason
		updateReason := "Metrics update"
		if significantPod != "" {
			updateReason = fmt.Sprintf("Metrics update for pod: %s", significantPod)
		}

		// Set the update reason
		mc.lastUpdateReason = updateReason

		select {
		case mc.updateCh <- struct{}{}:
			mc.logger.Debug("Notified listeners of significant metrics changes", map[string]interface{}{
				"reason": updateReason,
				"pod":    significantPod,
				"change": fmt.Sprintf("%.2f%%", maxChange*100),
			})
		default:
			// Channel already has an update queued, no need to send another
		}
	}
}

// GetPodMetrics retrieves metrics for a specific pod
func (mc *MetricsCollector) GetPodMetrics(namespace, name string) (string, string, float64, float64, models.TrendDirection, models.TrendDirection) {
	if !mc.metricsEnabled || mc.metricsClient == nil {
		return "n/a", "n/a", 0, 0, models.TrendStatic, models.TrendStatic
	}

	// Create the pod key
	podKey := fmt.Sprintf("%s/%s", namespace, name)

	// Read from cache with a read lock
	mc.metricsCacheMutex.RLock()
	defer mc.metricsCacheMutex.RUnlock()

	// Check if we have metrics for this pod
	metrics, exists := mc.metricsCache[podKey]
	if !exists {
		return "n/a", "n/a", 0, 0, models.TrendStatic, models.TrendStatic
	}

	return metrics.CPU, metrics.Memory, metrics.CPUValue, metrics.MemoryValue, metrics.CPUTrend, metrics.MemoryTrend
}

// calculateTrend analyzes a series of historical values to determine the trend
func (mc *MetricsCollector) calculateTrend(values []float64, threshold float64) models.TrendDirection {
	if len(values) < 2 {
		return models.TrendStatic
	}

	// Calculate average change between consecutive samples
	totalChange := 0.0
	changeCount := 0

	for i := 1; i < len(values); i++ {
		if values[i-1] > 0 { // Avoid division by zero
			change := values[i] - values[i-1]
			percentChange := change / values[i-1]
			totalChange += percentChange
			changeCount++
		}
	}

	// No valid changes to calculate trend
	if changeCount == 0 {
		return models.TrendStatic
	}

	// Calculate average percent change
	avgPercentChange := totalChange / float64(changeCount)

	// Determine trend based on average percent change
	if math.Abs(avgPercentChange) < threshold {
		return models.TrendStatic
	} else if avgPercentChange > 0 {
		return models.TrendUp
	} else {
		return models.TrendDown
	}
}

// formatMemoryBytes converts bytes to human-readable format
func formatMemoryBytes(bytes int64) string {
	// Memory bytes are calculated with binary SI
	// 1 KiB = 1024 bytes, 1 MiB = 1024 KiB, 1 GiB = 1024 MiB
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)

	if bytes < KiB {
		return fmt.Sprintf("%dB", bytes)
	} else if bytes < MiB {
		return fmt.Sprintf("%.1fKiB", float64(bytes)/KiB)
	} else if bytes < GiB {
		return fmt.Sprintf("%.1fMiB", float64(bytes)/MiB)
	} else {
		return fmt.Sprintf("%.1fGiB", float64(bytes)/GiB)
	}
}
