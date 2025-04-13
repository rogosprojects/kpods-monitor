package api

import (
	"context"
	"fmt"
	"kpods-monitor/pkg/log"
	"kpods-monitor/pkg/logger"
	"kpods-monitor/pkg/models"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Collector is responsible for gathering application data from Kubernetes
type Collector struct {
	clientset      *kubernetes.Clientset
	config         *models.Config
	metricsClient  *metricsv.Clientset
	metricsEnabled bool
}

// NewCollector creates a new Kubernetes data collector
func NewCollector(config *models.Config) (*Collector, error) {
	var k8sConfig *rest.Config
	var err error

	if config.Cluster.InCluster {
		// Use in-cluster config when running inside Kubernetes
		log.Println("Using in-cluster Kubernetes configuration")
		k8sConfig, err = rest.InClusterConfig()
	} else {
		// Use provided kubeconfig file and expand tilde if present
		kubeConfigPath := config.Cluster.KubeConfigPath
		if strings.HasPrefix(kubeConfigPath, "~/") {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to expand home directory: %w", err)
			}
			kubeConfigPath = filepath.Join(homeDir, kubeConfigPath[2:])
		}
		log.Printf("Using kubeconfig from: %s", kubeConfigPath)
		k8sConfig, err = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes config: %w", err)
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	collector := &Collector{
		clientset: clientset,
		config:    config,
	}

	// Initialize metrics client if available
	collector.initMetricsClient(k8sConfig)

	return collector, nil
}

// initMetricsClient initializes the metrics client if metrics-server is available
func (c *Collector) initMetricsClient(k8sConfig *rest.Config) {
	// Try to create metrics client using the provided config
	mClient, err := metricsv.NewForConfig(k8sConfig)
	if err != nil {
		logger.DefaultLogger.Error("Failed to create metrics client", err, nil)
		return
	}

	// Test if the metrics API is available
	_, err = mClient.MetricsV1beta1().PodMetricses("").List(context.Background(), metav1.ListOptions{Limit: 1})
	if err != nil {
		logger.DefaultLogger.Error("Metrics server not available", err, nil)
		return
	}

	// Set the metrics client and mark as available
	c.metricsClient = mClient
	c.metricsEnabled = true
	log.Println("Metrics server connected successfully")
}

// CollectApplications gathers data for all configured applications
func (c *Collector) CollectApplications() ([]models.Application, error) {
	var applications []models.Application
	var wg sync.WaitGroup

	// Create channels for results and errors
	appChan := make(chan models.Application, len(c.config.Applications))
	errChan := make(chan error, len(c.config.Applications))

	// Process each application configuration concurrently
	for _, appConfig := range c.config.Applications {
		wg.Add(1)
		go func(config models.ApplicationConfig) {
			defer wg.Done()

			app, err := c.collectApplicationData(config)
			if err != nil {
				log.Printf("Error collecting data for application %s: %v", config.Name, err)
				errChan <- fmt.Errorf("error collecting %s: %w", config.Name, err)
				return
			}

			appChan <- app
		}(appConfig)
	}

	// Wait for all collectors to finish
	wg.Wait()
	close(appChan)
	close(errChan)

	// Collect results
	for app := range appChan {
		applications = append(applications, app)
	}

	// Log any errors that occurred
	for err := range errChan {
		log.Printf("Collection error: %v", err)
	}

	return applications, nil
}

// collectApplicationData gathers data for a single application
func (c *Collector) collectApplicationData(appConfig models.ApplicationConfig) (models.Application, error) {
	order := appConfig.Order
	if order == 0 {
		// Default order to 1000 if not specified
		order = 1000
	}

	app := models.Application{
		Name:        appConfig.Name,
		Description: appConfig.Description,
		Pods:        []models.Pod{},
		Health:      models.HealthStatusHealthy, // Default to healthy
		Order:       order,
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Process each namespace defined in the selector
	for namespace, selector := range appConfig.Selector {
		// Store this namespace in the application
		found := false
		for _, ns := range app.Namespaces {
			if ns == namespace {
				found = true
				break
			}
		}
		if !found {
			app.Namespaces = append(app.Namespaces, namespace)
		}

		// Process workloads in parallel for this namespace
		var workloadWg sync.WaitGroup
		podsChan := make(chan []models.Pod, 5) // Buffer for 5 workload types

		// 1. Process Deployments
		if len(selector.Deployments) > 0 {
			workloadWg.Add(1)
			go func() {
				defer workloadWg.Done()
				pods, err := c.collectDeploymentPods(ctx, namespace, selector.Deployments)
				if err != nil {
					logger.DefaultLogger.Error("Failed to collect deployment pods", err, map[string]interface{}{
						"namespace": namespace,
					})
					return
				}
				podsChan <- pods
			}()
		}

		// 2. Process StatefulSets
		if len(selector.StatefulSets) > 0 {
			workloadWg.Add(1)
			go func() {
				defer workloadWg.Done()
				pods, err := c.collectStatefulSetPods(ctx, namespace, selector.StatefulSets)
				if err != nil {
					logger.DefaultLogger.Error("Failed to collect statefulset pods", err, map[string]interface{}{
						"namespace": namespace,
					})
					return
				}
				podsChan <- pods
			}()
		}

		// 3. Process DaemonSets
		if len(selector.DaemonSets) > 0 {
			workloadWg.Add(1)
			go func() {
				defer workloadWg.Done()
				pods, err := c.collectDaemonSetPods(ctx, namespace, selector.DaemonSets)
				if err != nil {
					logger.DefaultLogger.Error("Failed to collect daemonset pods", err, map[string]interface{}{
						"namespace": namespace,
					})
					return
				}
				podsChan <- pods
			}()
		}

		// 4. Process label selectors if provided
		if len(selector.Labels) > 0 {
			workloadWg.Add(1)
			go func() {
				defer workloadWg.Done()
				pods, err := c.collectPodsByLabels(ctx, namespace, selector.Labels)
				if err != nil {
					logger.DefaultLogger.Error("Failed to collect pods by labels", err, map[string]interface{}{
						"namespace": namespace,
					})
					return
				}
				podsChan <- pods
			}()
		}

		// 5. Process annotation selectors if provided
		if len(selector.Annotations) > 0 {
			workloadWg.Add(1)
			go func() {
				defer workloadWg.Done()
				pods, err := c.collectPodsByAnnotations(ctx, namespace, selector.Annotations)
				if err != nil {
					logger.DefaultLogger.Error("Failed to collect pods by annotations", err, map[string]interface{}{
						"namespace": namespace,
					})
					return
				}
				podsChan <- pods
			}()
		}

		// Start a collector goroutine for results
		var resultWg sync.WaitGroup
		resultWg.Add(1)
		var collectedPods []models.Pod

		go func() {
			defer resultWg.Done()
			for pods := range podsChan {
				collectedPods = append(collectedPods, pods...)
			}
		}()

		// Wait for workload collectors to finish
		workloadWg.Wait()
		close(podsChan)

		// Wait for results to be collected
		resultWg.Wait()

		// Add the collected pods to the application
		app.Pods = append(app.Pods, collectedPods...)
	}

	// Calculate application health based on pod statuses
	app.CalculateHealth()

	return app, nil
}

// collectDeploymentPods collects pods for specified deployments
func (c *Collector) collectDeploymentPods(ctx context.Context, namespace string, deploymentNames []string) ([]models.Pod, error) {
	var result []models.Pod
	var deploymentMap = make(map[string]*appsv1.Deployment)

	// Create a set for O(1) lookups
	deploymentSet := make(map[string]bool)
	for _, name := range deploymentNames {
		deploymentSet[name] = true
	}

	// 1. Get all deployments in the namespace
	deployments, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	// 2. Filter deployments by name
	var matchedDeployments []*appsv1.Deployment
	var missingDeployments []string

	// Track which deployments we've found
	foundDeployments := make(map[string]bool)

	for i, deployment := range deployments.Items {
		if deploymentSet[deployment.Name] {
			matchedDeployments = append(matchedDeployments, &deployments.Items[i])
			deploymentMap[deployment.Name] = &deployments.Items[i]
			foundDeployments[deployment.Name] = true
		}
	}

	// Find which deployments are missing
	for name := range deploymentSet {
		if !foundDeployments[name] {
			missingDeployments = append(missingDeployments, name)
		}
	}

	// 3. For each matched deployment, get pods
	for _, deployment := range matchedDeployments {
		// Get the pods for this deployment using label selector
		podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: metav1.FormatLabelSelector(deployment.Spec.Selector),
		})

		if err != nil {
			logger.DefaultLogger.Error("Failed to get pods for deployment", err, map[string]interface{}{
				"name": fmt.Sprintf("%s (namespace %s)", deployment.Name, namespace),
			})
			continue
		}

		// Process each pod
		for _, pod := range podList.Items {
			modelPod := c.convertPod(pod, "Deployment", deployment.Name)
			result = append(result, modelPod)
		}
	}

	// 4. Add missing deployments as missing pods
	for _, name := range missingDeployments {
		logger.DefaultLogger.Warn("Missing deployment", map[string]interface{}{
			"name": fmt.Sprintf("%s (namespace %s)", name, namespace),
		})
		missingPod := models.Pod{
			Name:      name,
			Status:    models.PodStatus("Unknown"),
			Kind:      "Deployment",
			Namespace: namespace,
			Missing:   true,
			CPU:       "n/a",
			Memory:    "n/a",
		}
		result = append(result, missingPod)
	}

	// 5. Add empty deployments (with zero pods) as warnings
	for _, deployment := range matchedDeployments {
		// Check if this deployment has any pods in our results
		hasPods := false
		for _, pod := range result {
			if pod.Kind == "Deployment" && pod.OwnerName == deployment.Name {
				hasPods = true
				break
			}
		}

		if !hasPods {
			logger.DefaultLogger.Error("Deployment has 0 running pods", nil, map[string]interface{}{
				"name": fmt.Sprintf("%s (namespace %s)", deployment.Name, namespace),
			})
			zeroPod := models.Pod{
				Name:      deployment.Name,
				Status:    models.PodStatus("Warning"),
				Kind:      "Deployment",
				Namespace: namespace,
				Missing:   false,
				ZeroPods:  true,
				CPU:       "n/a",
				Memory:    "n/a",
				OwnerName: deployment.Name,
			}
			result = append(result, zeroPod)
		}
	}

	return result, nil
}

// collectStatefulSetPods collects pods for specified statefulsets
func (c *Collector) collectStatefulSetPods(ctx context.Context, namespace string, statefulSetNames []string) ([]models.Pod, error) {
	var result []models.Pod

	// Create a set for O(1) lookups
	statefulSetSet := make(map[string]bool)
	for _, name := range statefulSetNames {
		statefulSetSet[name] = true
	}

	// 1. Get all statefulsets in the namespace
	statefulSets, err := c.clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list statefulsets: %w", err)
	}

	// 2. Filter statefulsets by name
	var matchedStatefulSets []*appsv1.StatefulSet
	var missingStatefulSets []string

	// Track which statefulsets we've found
	foundStatefulSets := make(map[string]bool)

	for i, statefulSet := range statefulSets.Items {
		if statefulSetSet[statefulSet.Name] {
			matchedStatefulSets = append(matchedStatefulSets, &statefulSets.Items[i])
			foundStatefulSets[statefulSet.Name] = true
		}
	}

	// Find which statefulsets are missing
	for name := range statefulSetSet {
		if !foundStatefulSets[name] {
			missingStatefulSets = append(missingStatefulSets, name)
		}
	}

	// 3. For each matched statefulset, get pods
	for _, statefulSet := range matchedStatefulSets {
		// Get the pods for this statefulset using label selector
		podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: metav1.FormatLabelSelector(statefulSet.Spec.Selector),
		})

		if err != nil {
			logger.DefaultLogger.Warn("Failed to get pods for statefulset", map[string]interface{}{
				"name": fmt.Sprintf("%s (namespace %s)", statefulSet.Name, namespace),
			})
			continue
		}

		// Process each pod
		for _, pod := range podList.Items {
			modelPod := c.convertPod(pod, "StatefulSet", statefulSet.Name)
			result = append(result, modelPod)
		}
	}

	// 4. Add missing statefulsets as missing pods
	for _, name := range missingStatefulSets {
		logger.DefaultLogger.Warn("Missing statefulset", map[string]interface{}{
			"name": fmt.Sprintf("%s (namespace %s)", name, namespace),
		})
		missingPod := models.Pod{
			Name:      name,
			Status:    models.PodStatus("Unknown"),
			Kind:      "StatefulSet",
			Namespace: namespace,
			Missing:   true,
			CPU:       "n/a",
			Memory:    "n/a",
		}
		result = append(result, missingPod)
	}

	// 5. Add empty statefulsets (with zero pods) as warnings
	for _, statefulSet := range matchedStatefulSets {
		// Check if this statefulset has any pods in our results
		hasPods := false
		for _, pod := range result {
			if pod.Kind == "StatefulSet" && pod.OwnerName == statefulSet.Name {
				hasPods = true
				break
			}
		}

		if !hasPods {
			logger.DefaultLogger.Error("StatefulSet has 0 running pods", nil, map[string]interface{}{
				"name": fmt.Sprintf("%s (namespace %s)", statefulSet.Name, namespace),
			})
			zeroPod := models.Pod{
				Name:      statefulSet.Name,
				Status:    models.PodStatus("Warning"),
				Kind:      "StatefulSet",
				Namespace: namespace,
				Missing:   false,
				ZeroPods:  true,
				CPU:       "n/a",
				Memory:    "n/a",
				OwnerName: statefulSet.Name,
			}
			result = append(result, zeroPod)
		}
	}

	return result, nil
}

// collectDaemonSetPods collects pods for specified daemonsets
func (c *Collector) collectDaemonSetPods(ctx context.Context, namespace string, daemonSetNames []string) ([]models.Pod, error) {
	var result []models.Pod

	// Create a set for O(1) lookups
	daemonSetSet := make(map[string]bool)
	for _, name := range daemonSetNames {
		daemonSetSet[name] = true
	}

	// 1. Get all daemonsets in the namespace
	daemonSets, err := c.clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list daemonsets: %w", err)
	}

	// 2. Filter daemonsets by name
	var matchedDaemonSets []*appsv1.DaemonSet
	var missingDaemonSets []string

	// Track which daemonsets we've found
	foundDaemonSets := make(map[string]bool)

	for i, daemonSet := range daemonSets.Items {
		if daemonSetSet[daemonSet.Name] {
			matchedDaemonSets = append(matchedDaemonSets, &daemonSets.Items[i])
			foundDaemonSets[daemonSet.Name] = true
		}
	}

	// Find which daemonsets are missing
	for name := range daemonSetSet {
		if !foundDaemonSets[name] {
			missingDaemonSets = append(missingDaemonSets, name)
		}
	}

	// 3. For each matched daemonset, get pods
	for _, daemonSet := range matchedDaemonSets {
		// Get the pods for this daemonset using label selector
		podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: metav1.FormatLabelSelector(daemonSet.Spec.Selector),
		})

		if err != nil {
			logger.DefaultLogger.Warn("Failed to get pods for daemonset", map[string]interface{}{
				"name": fmt.Sprintf("%s (namespace %s)", daemonSet.Name, namespace),
			})
			continue
		}

		// Process each pod
		for _, pod := range podList.Items {
			modelPod := c.convertPod(pod, "DaemonSet", daemonSet.Name)
			result = append(result, modelPod)
		}
	}

	// 4. Add missing daemonsets as missing pods
	for _, name := range missingDaemonSets {
		logger.DefaultLogger.Warn("Missing daemonset", map[string]interface{}{
			"name": fmt.Sprintf("%s (namespace %s)", name, namespace),
		})
		missingPod := models.Pod{
			Name:      name,
			Status:    models.PodStatus("Unknown"),
			Kind:      "DaemonSet",
			Namespace: namespace,
			Missing:   true,
			CPU:       "n/a",
			Memory:    "n/a",
		}
		result = append(result, missingPod)
	}

	// 5. Add empty daemonsets (with zero pods) as warnings
	for _, daemonSet := range matchedDaemonSets {
		// Check if this daemonset has any pods in our results
		hasPods := false
		for _, pod := range result {
			if pod.Kind == "DaemonSet" && pod.OwnerName == daemonSet.Name {
				hasPods = true
				break
			}
		}

		if !hasPods {
			logger.DefaultLogger.Error("DaemonSet has 0 running pods", nil, map[string]interface{}{
				"name": fmt.Sprintf("%s (namespace %s)", daemonSet.Name, namespace),
			})
			zeroPod := models.Pod{
				Name:      daemonSet.Name,
				Status:    models.PodStatus("Warning"),
				Kind:      "DaemonSet",
				Namespace: namespace,
				Missing:   false,
				ZeroPods:  true,
				CPU:       "n/a",
				Memory:    "n/a",
				OwnerName: daemonSet.Name,
			}
			result = append(result, zeroPod)
		}
	}

	return result, nil
}

// collectPodsByLabels collects pods that match the given labels
func (c *Collector) collectPodsByLabels(ctx context.Context, namespace string, labels map[string]string) ([]models.Pod, error) {
	var result []models.Pod

	// Build label selector string
	var labelSelector string
	if len(labels) > 0 {
		selectors := []string{}
		for k, v := range labels {
			selectors = append(selectors, fmt.Sprintf("%s=%s", k, v))
		}
		labelSelector = strings.Join(selectors, ",")
	}

	// Get pods with the label selector
	podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list pods by labels: %w", err)
	}

	// Process each pod
	for _, pod := range podList.Items {
		// Determine the kind and owner name
		kind, ownerName := c.determineOwner(pod)
		modelPod := c.convertPod(pod, kind, ownerName)
		result = append(result, modelPod)
	}

	return result, nil
}

// collectPodsByAnnotations collects pods that match the given annotations
func (c *Collector) collectPodsByAnnotations(ctx context.Context, namespace string, annotations map[string]string) ([]models.Pod, error) {
	var result []models.Pod

	// We need to list all pods and filter by annotations, as Kubernetes API doesn't support annotation filtering
	podList, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for annotation filtering: %w", err)
	}

	// Process each pod and check annotations
	for _, pod := range podList.Items {
		// Check if pod has all the required annotations
		matches := true
		for key, value := range annotations {
			if podValue, exists := pod.Annotations[key]; !exists || podValue != value {
				matches = false
				break
			}
		}

		if matches {
			// Determine the kind and owner name
			kind, ownerName := c.determineOwner(pod)
			modelPod := c.convertPod(pod, kind, ownerName)
			result = append(result, modelPod)
		}
	}

	return result, nil
}

// determineOwner extracts the kind and name of the workload that owns this pod
// Note: only for Pods collected by labels or annotations
func (c *Collector) determineOwner(pod corev1.Pod) (string, string) {
	// Check for ownership references
	for _, owner := range pod.OwnerReferences {
		switch owner.Kind {
		case "ReplicaSet":
			// For ReplicaSet, need to determine if it's part of a Deployment
			// This requires an additional API call
			rs, err := c.clientset.AppsV1().ReplicaSets(pod.Namespace).Get(
				context.Background(), owner.Name, metav1.GetOptions{})
			if err == nil {
				// Check if the ReplicaSet is owned by a Deployment
				for _, rsOwner := range rs.OwnerReferences {
					if rsOwner.Kind == "Deployment" {
						return "Deployment", rsOwner.Name
					}
				}
			}
			return "ReplicaSet", owner.Name
		case "StatefulSet":
			return "StatefulSet", owner.Name
		case "DaemonSet":
			return "DaemonSet", owner.Name
		case "Job":
			// Check if Job is owned by a CronJob
			job, err := c.clientset.BatchV1().Jobs(pod.Namespace).Get(
				context.Background(), owner.Name, metav1.GetOptions{})
			if err == nil {
				for _, jobOwner := range job.OwnerReferences {
					if jobOwner.Kind == "CronJob" {
						return "CronJob", jobOwner.Name
					}
				}
			}
			return "Job", owner.Name
		}
	}

	// If no owner reference, try to determine from pod name patterns
	if strings.Contains(pod.Name, "-") {
		parts := strings.Split(pod.Name, "-")
		if len(parts) >= 3 {
			// Check for StatefulSet pattern (name-ordinal)
			lastPart := parts[len(parts)-1]
			if _, err := fmt.Sscanf(lastPart, "%d", new(int)); err == nil && len(parts) >= 2 {
				// This might be a StatefulSet pod (name-0, name-1, etc.)
				statefulSetName := strings.Join(parts[:len(parts)-1], "-")
				return "StatefulSet", statefulSetName
			}
		}
	}

	// Default to standalone pod
	return "Pod", pod.Name
}

// convertPod converts a Kubernetes Pod to our internal Pod model
func (c *Collector) convertPod(pod corev1.Pod, kind string, ownerName string) models.Pod {
	// Determine pod status
	status := c.determinePodStatus(pod)

	// Calculate pod age
	age := c.calculatePodAge(pod.CreationTimestamp.Time)

	// Count restarts
	restarts := c.countRestarts(pod)

	// Get CPU and memory usage from metrics-server if available
	cpu, memory, cpuValue, memoryValue, cpuTrend, memoryTrend := c.getPodMetrics(pod.Namespace, pod.Name)

	return models.Pod{
		Name:        pod.Name,
		Status:      status,
		StartTime:   pod.CreationTimestamp.Time,
		Age:         age,
		Restarts:    restarts,
		CPU:         cpu,
		CPUValue:    cpuValue,
		CPUTrend:    cpuTrend,
		Memory:      memory,
		MemoryValue: memoryValue,
		MemoryTrend: memoryTrend,
		Kind:        kind, // Use the determined workload kind
		Namespace:   pod.Namespace,
		OwnerName:   ownerName, // Include the owner name for better identification
	}
}

// determinePodStatus maps Kubernetes pod phase to our internal status
func (c *Collector) determinePodStatus(pod corev1.Pod) models.PodStatus {
	// Check for special conditions first
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionFalse {
			if condition.Reason == "ContainersNotReady" {
				// Check for CrashLoopBackOff in container statuses
				for _, containerStatus := range pod.Status.ContainerStatuses {
					if containerStatus.State.Waiting != nil &&
						containerStatus.State.Waiting.Reason == "CrashLoopBackOff" {
						return models.PodStatusCrashLoopBackOff
					}
				}
			}
		}
	}

	// Check if pod is being deleted
	if pod.DeletionTimestamp != nil {
		return models.PodStatusTerminating
	}

	// Map standard pod phases
	switch pod.Status.Phase {
	case corev1.PodRunning:
		return models.PodStatusRunning
	case corev1.PodPending:
		return models.PodStatusPending
	case corev1.PodFailed:
		return models.PodStatusError
	case corev1.PodSucceeded:
		return models.PodStatusCompleted
	default:
		return models.PodStatusPending
	}
}

// calculatePodAge returns a human-readable age string
func (c *Collector) calculatePodAge(startTime time.Time) string {
	duration := time.Since(startTime)

	days := int(duration.Hours()) / 24
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}

	hours := int(duration.Hours())
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}

	minutes := int(duration.Minutes())
	return fmt.Sprintf("%dm", minutes)
}

// countRestarts counts the total number of container restarts in a pod
func (c *Collector) countRestarts(pod corev1.Pod) int {
	total := 0
	for _, containerStatus := range pod.Status.ContainerStatuses {
		total += int(containerStatus.RestartCount)
	}
	return total
}

// Metrics cache for tracking trends
type metricsCacheEntry struct {
	cpuValues    []float64
	memoryValues []float64
	timestamps   []time.Time
	lastUpdated  time.Time
}

var (
	podMetricsCache      = make(map[string]metricsCacheEntry)
	podMetricsCacheMutex sync.RWMutex
	numMetricSamples     = 3    // Number of samples to keep for trend calculation
	cpuTrendThreshold    = 0.30 // percent threshold for CPU trend
	memoryTrendThreshold = 0.10 // percent threshold for memory trend
)

// getPodMetrics fetches resource usage metrics for a pod
func (c *Collector) getPodMetrics(namespace, name string) (string, string, float64, float64, models.TrendDirection, models.TrendDirection) {
	if !c.metricsEnabled || c.metricsClient == nil {
		return "n/a", "n/a", 0, 0, models.TrendStatic, models.TrendStatic
	}

	// Use a timeout context to prevent hanging
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to get pod metrics
	podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses(namespace).Get(
		ctx, name, metav1.GetOptions{})
	if err != nil {
		return "n/a", "n/a", 0, 0, models.TrendStatic, models.TrendStatic
	}

	// Calculate total CPU and memory usage across all containers
	var cpuUsage int64
	var memoryUsage int64

	for _, container := range podMetrics.Containers {
		// CPU is in "n" format, typically in nanocores (n)
		cpu := container.Usage.Cpu().MilliValue()
		cpuUsage += cpu

		// Memory is in bytes / KiB / MiB / GiB (Binary SI)
		memory := container.Usage.Memory().Value()
		memoryUsage += memory
	}

	// Get previous metrics from cache to calculate trend
	cpuTrend := models.TrendStatic
	memoryTrend := models.TrendStatic

	// Look up previous values from cache
	podKey := fmt.Sprintf("%s/%s", namespace, name)

	// Read from cache with a read lock
	podMetricsCacheMutex.RLock()
	cacheEntry, exists := podMetricsCache[podKey]
	podMetricsCacheMutex.RUnlock()

	// Calculate trends using historical samples if we have cached data
	if exists && len(cacheEntry.cpuValues) > 0 {
		// Calculate CPU trend using historical samples
		cpuTrend = calculateMetricTrend(cacheEntry.cpuValues, float64(cpuUsage), cpuTrendThreshold)

		// Calculate memory trend using historical samples
		memoryTrend = calculateMetricTrend(cacheEntry.memoryValues, float64(memoryUsage), memoryTrendThreshold)
	}

	// Update cache with current values for next time (using a write lock)
	podMetricsCacheMutex.Lock()

	if !exists {
		// First time seeing this pod, initialize cache entry
		podMetricsCache[podKey] = metricsCacheEntry{
			cpuValues:    []float64{float64(cpuUsage)},
			memoryValues: []float64{float64(memoryUsage)},
			timestamps:   []time.Time{time.Now()},
			lastUpdated:  time.Now(),
		}
	} else {
		// Update existing cache entry
		newCPUValues := append(cacheEntry.cpuValues, float64(cpuUsage))
		newMemoryValues := append(cacheEntry.memoryValues, float64(memoryUsage))
		newTimestamps := append(cacheEntry.timestamps, time.Now())

		// Debug values lengths
		// log.Printf("New CPU values length: %d", len(newCPUValues))
		// log.Printf("New Memory values length: %d", len(newMemoryValues))
		// log.Printf("New Timestamps length: %d", len(newTimestamps))
		// Keep only the last numMetricSamples samples
		if len(newCPUValues) > numMetricSamples {
			newCPUValues = newCPUValues[len(newCPUValues)-numMetricSamples:]
			newMemoryValues = newMemoryValues[len(newMemoryValues)-numMetricSamples:]
			newTimestamps = newTimestamps[len(newTimestamps)-numMetricSamples:]
		}

		podMetricsCache[podKey] = metricsCacheEntry{
			cpuValues:    newCPUValues,
			memoryValues: newMemoryValues,
			timestamps:   newTimestamps,
			lastUpdated:  time.Now(),
		}
	}

	podMetricsCacheMutex.Unlock()

	// Format CPU and memory for display
	cpuStr := fmt.Sprintf("%dm", cpuUsage)
	memoryStr := formatMemory(memoryUsage)

	return cpuStr, memoryStr, float64(cpuUsage), float64(memoryUsage), cpuTrend, memoryTrend
}

// calculateMetricTrend analyzes a series of historical values to determine the trend
func calculateMetricTrend(historicalValues []float64, currentValue float64, threshold float64) models.TrendDirection {
	if len(historicalValues) == 0 {
		return models.TrendStatic
	}

	// If we only have one historical value, do a simple comparison
	if len(historicalValues) == 1 {
		change := currentValue - historicalValues[0]
		percentChange := math.Abs(change) / historicalValues[0]

		if percentChange < threshold {
			return models.TrendStatic
		} else if change > 0 {
			return models.TrendUp
		} else {
			return models.TrendDown
		}
	}

	// With multiple samples, calculate a more robust trend
	allValues := append(historicalValues, currentValue)

	// Calculate average change between consecutive samples
	totalChange := 0.0
	changeCount := 0

	for i := 1; i < len(allValues); i++ {
		if allValues[i-1] > 0 { // Avoid division by zero
			change := allValues[i] - allValues[i-1]
			percentChange := change / allValues[i-1]
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

// formatMemory converts bytes to human-readable format
func formatMemory(bytes int64) string {
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
