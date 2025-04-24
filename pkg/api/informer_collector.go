package api

import (
	"context"
	"fmt"
	"kpods-monitor/pkg/logger"
	"kpods-monitor/pkg/models"
	"sort"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// InformerCollector uses Kubernetes informers to efficiently watch for changes
type InformerCollector struct {
	clientset      *kubernetes.Clientset
	config         *models.Config
	metricsClient  *metricsv.Clientset
	metricsEnabled bool
	logger         *logger.Logger

	// Map of namespace to informer factory
	factories map[string]informers.SharedInformerFactory

	// Maps of namespace to pod informers
	podInformers map[string]cache.SharedIndexInformer

	// Maps of namespace to pod stores
	podStores map[string]cache.Store

	// Metrics collector for efficient metrics collection
	metricsCollector *MetricsCollector

	// Channel for notifying about updates
	updateCh chan struct{}

	// Debounce timer for coalescing multiple updates
	debounceTimer     *time.Timer
	debounceTimerLock sync.Mutex
	debounceInterval  time.Duration

	// Context for controlling informers
	ctx    context.Context
	cancel context.CancelFunc

	// State tracking
	mutex   sync.RWMutex
	running bool

	// Track the reason for the most recent update
	lastUpdateReason string
	updateReasonLock sync.RWMutex

	// Track which namespaces we're watching
	watchedNamespaces map[string]bool
}

// NewInformerCollector creates a new collector that uses Kubernetes informers
func NewInformerCollector(collector *Collector) (*InformerCollector, error) {
	// Create a cancellable context for the informers
	ctx, cancel := context.WithCancel(context.Background())

	// Create the metrics collector
	metricsCollector := NewMetricsCollector(collector.metricsClient, collector.metricsEnabled, collector.config)

	ic := &InformerCollector{
		clientset:         collector.clientset,
		config:            collector.config,
		metricsClient:     collector.metricsClient,
		metricsEnabled:    collector.metricsEnabled,
		logger:            logger.DefaultLogger,
		metricsCollector:  metricsCollector,
		updateCh:          make(chan struct{}, 10), // Increased buffer size to handle more updates
		debounceInterval:  500 * time.Millisecond,  // Debounce interval of 500ms
		ctx:               ctx,
		cancel:            cancel,
		factories:         make(map[string]informers.SharedInformerFactory),
		podInformers:      make(map[string]cache.SharedIndexInformer),
		podStores:         make(map[string]cache.Store),
		watchedNamespaces: make(map[string]bool),
	}

	// Extract namespaces from the config
	for _, appConfig := range ic.config.Applications {
		for namespace := range appConfig.Selector {
			ic.watchedNamespaces[namespace] = true
		}
	}

	// Log the namespaces we're watching
	var namespaces []string
	for namespace := range ic.watchedNamespaces {
		namespaces = append(namespaces, namespace)
	}
	ic.logger.Info("Setting up namespace-scoped informers", map[string]interface{}{
		"namespaces": namespaces,
	})

	// Create informers for each namespace
	for namespace := range ic.watchedNamespaces {
		// Create a namespace-scoped factory
		factory := informers.NewSharedInformerFactoryWithOptions(
			ic.clientset,
			0, // No resync period
			informers.WithNamespace(namespace),
		)

		ic.factories[namespace] = factory

		// Create only pod informers for each namespace - we only need to watch pods for UI updates
		podInformer := factory.Core().V1().Pods().Informer()

		// Store the pod informer
		ic.podInformers[namespace] = podInformer

		// Get store from pod informer
		ic.podStores[namespace] = podInformer.GetStore()

		// For controller information, we'll use the Kubernetes API directly when needed
		// This simplifies our implementation and reduces resource usage

		// Add event handlers for pod informer
		podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				if pod, ok := obj.(*corev1.Pod); ok {
					// Only queue updates for pods that are part of configured workloads
					if ic.isPodWatched(pod.Namespace, pod.Name) {
						ic.queueUpdate(fmt.Sprintf("Pod added: %s/%s", pod.Namespace, pod.Name))
					} else {
						ic.logger.Debug("Ignoring pod add event - not part of watched workloads", map[string]interface{}{
							"pod": fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
						})
					}
				}
			},
			UpdateFunc: func(old, new interface{}) {
				if pod, ok := new.(*corev1.Pod); ok {
					// Only queue updates for pods that are part of configured workloads
					if ic.isPodWatched(pod.Namespace, pod.Name) {
						ic.queueUpdate(fmt.Sprintf("Pod updated: %s/%s", pod.Namespace, pod.Name))
					} else {
						ic.logger.Debug("Ignoring pod update event - not part of watched workloads", map[string]interface{}{
							"pod": fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
						})
					}
				}
			},
			DeleteFunc: func(obj interface{}) {
				if pod, ok := obj.(*corev1.Pod); ok {
					// Only queue updates for pods that are part of configured workloads
					if ic.isPodWatched(pod.Namespace, pod.Name) {
						ic.queueUpdate(fmt.Sprintf("Pod deleted: %s/%s", pod.Namespace, pod.Name))
					} else {
						ic.logger.Debug("Ignoring pod delete event - not part of watched workloads", map[string]interface{}{
							"pod": fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
						})
					}
				} else if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					if pod, ok := tombstone.Obj.(*corev1.Pod); ok {
						// Only queue updates for pods that are part of configured workloads
						if ic.isPodWatched(pod.Namespace, pod.Name) {
							ic.queueUpdate(fmt.Sprintf("Pod deleted: %s/%s", pod.Namespace, pod.Name))
						} else {
							ic.logger.Debug("Ignoring pod delete event - not part of watched workloads", map[string]interface{}{
								"pod": fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
							})
						}
					}
				}
			},
		})

		// We no longer need event handlers for controllers (Deployments, StatefulSets, DaemonSets)
		// Pod events are sufficient for UI updates, and we'll get controller information when needed
	}

	return ic, nil
}

// isPodWatched checks if a pod belongs to a workload that's configured to be displayed
// This is a wrapper around the MetricsCollector's isPodWatched method to avoid code duplication
func (ic *InformerCollector) isPodWatched(namespace, podName string) bool {
	// Create a fake PodMetrics object with just the namespace and name
	fakePodMetrics := &metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
	}

	// Use the MetricsCollector's isPodWatched method
	return ic.metricsCollector.IsPodWatched(fakePodMetrics)
}

// queueUpdate sends an update notification with debouncing to coalesce multiple updates
func (ic *InformerCollector) queueUpdate(reason string) {
	// Use debouncing to coalesce multiple updates within a short time window
	ic.debounceTimerLock.Lock()
	defer ic.debounceTimerLock.Unlock()

	// Store the reason for this update
	ic.updateReasonLock.Lock()
	ic.lastUpdateReason = reason
	ic.updateReasonLock.Unlock()

	// If timer is already running, stop it and create a new one
	if ic.debounceTimer != nil {
		ic.debounceTimer.Stop()
	}

	// Create a new timer that will send the update after the debounce interval
	ic.debounceTimer = time.AfterFunc(ic.debounceInterval, func() {
		// When the timer fires, send the update
		select {
		case ic.updateCh <- struct{}{}:
			// Successfully queued update
			ic.logger.Debug("Queued update notification from informer (after debounce)", map[string]interface{}{
				"reason": reason,
			})
		default:
			// Channel already has an update queued, no need to send another
			ic.logger.Debug("Update channel full, skipping update", map[string]interface{}{
				"reason": reason,
			})
		}
	})

	ic.logger.Debug("Scheduled debounced update", map[string]interface{}{
		"reason":            reason,
		"debounce_interval": ic.debounceInterval.String(),
	})
}

// Start begins watching for Kubernetes resource changes
func (ic *InformerCollector) Start() error {
	ic.mutex.Lock()
	defer ic.mutex.Unlock()

	if ic.running {
		return nil // Already running
	}

	// Start all informers for each namespace
	for namespace, factory := range ic.factories {
		ic.logger.Debug("Starting informers for namespace", map[string]interface{}{
			"namespace": namespace,
		})
		factory.Start(ic.ctx.Done())
	}

	// Wait for all caches to sync
	for namespace := range ic.factories {
		ic.logger.Debug("Waiting for caches to sync for namespace", map[string]interface{}{
			"namespace": namespace,
		})

		// Get pod informer for this namespace
		podInformer := ic.podInformers[namespace]

		// Wait for pod cache to sync
		if !cache.WaitForCacheSync(ic.ctx.Done(), podInformer.HasSynced) {
			return fmt.Errorf("failed to sync pod informer cache for namespace %s", namespace)
		}
	}

	// Start the metrics collector
	ic.metricsCollector.Start()

	// Set up a listener for metrics updates
	go ic.listenForMetricsUpdates()

	ic.running = true
	ic.logger.Info("Kubernetes informers started successfully", map[string]interface{}{
		"namespaces": len(ic.watchedNamespaces),
	})

	// Queue an initial update
	ic.queueUpdate("Initial startup - all namespaces")

	return nil
}

// listenForMetricsUpdates listens for updates from the metrics collector
func (ic *InformerCollector) listenForMetricsUpdates() {
	metricsUpdateCh := ic.metricsCollector.GetUpdateChannel()

	for {
		select {
		case <-metricsUpdateCh:
			// When metrics have significant changes, queue an update
			// Get the reason for the update from the metrics collector
			updateReason := ic.metricsCollector.GetLastUpdateReason()
			if updateReason == "" {
				updateReason = "Metrics update"
			}

			ic.logger.Debug("Received metrics update, queueing data refresh", map[string]interface{}{
				"reason": updateReason,
			})

			ic.queueUpdate(updateReason)
		case <-ic.ctx.Done():
			// Context cancelled, stop listening
			return
		}
	}
}

// Stop stops the informers and metrics collector
func (ic *InformerCollector) Stop() {
	ic.mutex.Lock()
	defer ic.mutex.Unlock()

	if !ic.running {
		return // Not running
	}

	// Stop the metrics collector
	ic.metricsCollector.Stop()

	// Cancel the context to stop all informers
	ic.cancel()
	ic.running = false
	ic.logger.Info("Kubernetes informers stopped", map[string]interface{}{
		"namespaces": len(ic.watchedNamespaces),
	})
}

// GetUpdateChannel returns the channel that signals when updates are available
func (ic *InformerCollector) GetUpdateChannel() <-chan struct{} {
	return ic.updateCh
}

// GetLastUpdateReason returns the reason for the most recent update
func (ic *InformerCollector) GetLastUpdateReason() string {
	ic.updateReasonLock.RLock()
	defer ic.updateReasonLock.RUnlock()
	return ic.lastUpdateReason
}

// CollectApplications gathers data for all configured applications using the cached data
func (ic *InformerCollector) CollectApplications() ([]models.Application, error) {
	var applications []models.Application

	// Check if informers are running
	ic.mutex.RLock()
	running := ic.running
	ic.mutex.RUnlock()

	if !running {
		return nil, fmt.Errorf("informers are not running")
	}

	// Process each application configuration
	for _, appConfig := range ic.config.Applications {
		app, err := ic.collectApplicationData(appConfig)
		if err != nil {
			ic.logger.Error("Error collecting data for application", err, map[string]interface{}{
				"app_name": appConfig.Name,
			})
			continue
		}

		applications = append(applications, app)
	}

	return applications, nil
}

// collectApplicationData gathers data for a single application
func (ic *InformerCollector) collectApplicationData(appConfig models.ApplicationConfig) (models.Application, error) {
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

		var collectedPods []models.Pod

		// 1. Process Deployments
		if len(selector.Deployments) > 0 {
			pods, err := ic.collectDeploymentPods(namespace, selector.Deployments)
			if err != nil {
				ic.logger.Error("Failed to collect deployment pods", err, map[string]interface{}{
					"namespace": namespace,
				})
			} else {
				collectedPods = append(collectedPods, pods...)
			}
		}

		// 2. Process StatefulSets
		if len(selector.StatefulSets) > 0 {
			pods, err := ic.collectStatefulSetPods(namespace, selector.StatefulSets)
			if err != nil {
				ic.logger.Error("Failed to collect statefulset pods", err, map[string]interface{}{
					"namespace": namespace,
				})
			} else {
				collectedPods = append(collectedPods, pods...)
			}
		}

		// 3. Process DaemonSets
		if len(selector.DaemonSets) > 0 {
			pods, err := ic.collectDaemonSetPods(namespace, selector.DaemonSets)
			if err != nil {
				ic.logger.Error("Failed to collect daemonset pods", err, map[string]interface{}{
					"namespace": namespace,
				})
			} else {
				collectedPods = append(collectedPods, pods...)
			}
		}

		// Add the collected pods to the application
		app.Pods = append(app.Pods, collectedPods...)
	}

	// Sort pods by name for consistent display
	sort.Slice(app.Pods, func(i, j int) bool {
		return app.Pods[i].Name < app.Pods[j].Name
	})

	// Calculate application health based on pod statuses
	app.CalculateHealth()

	return app, nil
}

// collectDeploymentPods collects pods for specified deployments using the Kubernetes API directly
func (ic *InformerCollector) collectDeploymentPods(namespace string, deploymentNames []string) ([]models.Pod, error) {
	var result []models.Pod

	// Create a set for O(1) lookups
	deploymentSet := make(map[string]bool)
	for _, name := range deploymentNames {
		deploymentSet[name] = true
	}

	// Find matching deployments using the Kubernetes API
	var matchedDeployments []*appsv1.Deployment
	var missingDeployments []string
	foundDeployments := make(map[string]bool)

	// Get deployments directly from the Kubernetes API
	for deployName := range deploymentSet {
		// Get the deployment from the API
		deploy, err := ic.clientset.AppsV1().Deployments(namespace).Get(ic.ctx, deployName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				missingDeployments = append(missingDeployments, deployName)
				continue
			}
			ic.logger.Error("Failed to get deployment", err, map[string]interface{}{
				"deployment": deployName,
				"namespace":  namespace,
			})
			continue
		}

		// Mark as found and add to matched deployments
		foundDeployments[deployName] = true
		matchedDeployments = append(matchedDeployments, deploy)
	}

	// Check if we have a pod store for this namespace
	podStore, ok := ic.podStores[namespace]
	if !ok {
		return nil, fmt.Errorf("no pod store found for namespace %s", namespace)
	}

	// For each matched deployment, get pods
	for _, deployment := range matchedDeployments {
		// Get the label selector for this deployment
		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			ic.logger.Error("Failed to parse deployment selector", err, map[string]interface{}{
				"deployment": deployment.Name,
				"namespace":  namespace,
			})
			continue
		}

		// Find pods matching this selector
		pods := podStore.List()
		var matchingPods []corev1.Pod
		for _, obj := range pods {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				continue // Skip if not a pod
			}

			// Check if pod matches the selector (we already know it's in the right namespace)
			podLabels := labels.Set(pod.Labels)
			if selector.Matches(podLabels) {
				matchingPods = append(matchingPods, *pod)
			}
		}

		// Process each pod
		for _, pod := range matchingPods {
			modelPod := ic.convertPod(pod, "Deployment", deployment.Name)
			result = append(result, modelPod)
		}

		// Check if this deployment has any pods
		if len(matchingPods) == 0 {
			ic.logger.Error("Deployment has 0 running pods", nil, map[string]interface{}{
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

	// Add missing deployments as missing pods
	for _, name := range missingDeployments {
		ic.logger.Warn("Missing deployment", map[string]interface{}{
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

	return result, nil
}

// collectStatefulSetPods collects pods for specified statefulsets using the Kubernetes API directly
func (ic *InformerCollector) collectStatefulSetPods(namespace string, statefulSetNames []string) ([]models.Pod, error) {
	var result []models.Pod

	// Create a set for O(1) lookups
	statefulSetSet := make(map[string]bool)
	for _, name := range statefulSetNames {
		statefulSetSet[name] = true
	}

	// Find matching statefulsets using the Kubernetes API
	var matchedStatefulSets []*appsv1.StatefulSet
	var missingStatefulSets []string
	foundStatefulSets := make(map[string]bool)

	// Get statefulsets directly from the Kubernetes API
	for ssName := range statefulSetSet {
		// Get the statefulset from the API
		ss, err := ic.clientset.AppsV1().StatefulSets(namespace).Get(ic.ctx, ssName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				missingStatefulSets = append(missingStatefulSets, ssName)
				continue
			}
			ic.logger.Error("Failed to get statefulset", err, map[string]interface{}{
				"statefulset": ssName,
				"namespace":   namespace,
			})
			continue
		}

		// Mark as found and add to matched statefulsets
		foundStatefulSets[ssName] = true
		matchedStatefulSets = append(matchedStatefulSets, ss)
	}

	// Check if we have a pod store for this namespace
	podStore, ok := ic.podStores[namespace]
	if !ok {
		return nil, fmt.Errorf("no pod store found for namespace %s", namespace)
	}

	// For each matched statefulset, get pods
	for _, statefulSet := range matchedStatefulSets {
		// Get the label selector for this statefulset
		selector, err := metav1.LabelSelectorAsSelector(statefulSet.Spec.Selector)
		if err != nil {
			ic.logger.Error("Failed to parse statefulset selector", err, map[string]interface{}{
				"statefulset": statefulSet.Name,
				"namespace":   namespace,
			})
			continue
		}

		// Find pods matching this selector
		pods := podStore.List()
		var matchingPods []corev1.Pod
		for _, obj := range pods {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				continue // Skip if not a pod
			}

			// Check if pod matches the selector (we already know it's in the right namespace)
			podLabels := labels.Set(pod.Labels)
			if selector.Matches(podLabels) {
				matchingPods = append(matchingPods, *pod)
			}
		}

		// Process each pod
		for _, pod := range matchingPods {
			modelPod := ic.convertPod(pod, "StatefulSet", statefulSet.Name)
			result = append(result, modelPod)
		}

		// Check if this statefulset has any pods
		if len(matchingPods) == 0 {
			ic.logger.Error("StatefulSet has 0 running pods", nil, map[string]interface{}{
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

	// Add missing statefulsets as missing pods
	for _, name := range missingStatefulSets {
		ic.logger.Warn("Missing statefulset", map[string]interface{}{
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

	return result, nil
}

// collectDaemonSetPods collects pods for specified daemonsets using the Kubernetes API directly
func (ic *InformerCollector) collectDaemonSetPods(namespace string, daemonSetNames []string) ([]models.Pod, error) {
	var result []models.Pod

	// Create a set for O(1) lookups
	daemonSetSet := make(map[string]bool)
	for _, name := range daemonSetNames {
		daemonSetSet[name] = true
	}

	// Find matching daemonsets using the Kubernetes API
	var matchedDaemonSets []*appsv1.DaemonSet
	var missingDaemonSets []string
	foundDaemonSets := make(map[string]bool)

	// Get daemonsets directly from the Kubernetes API
	for dsName := range daemonSetSet {
		// Get the daemonset from the API
		ds, err := ic.clientset.AppsV1().DaemonSets(namespace).Get(ic.ctx, dsName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				missingDaemonSets = append(missingDaemonSets, dsName)
				continue
			}
			ic.logger.Error("Failed to get daemonset", err, map[string]interface{}{
				"daemonset": dsName,
				"namespace": namespace,
			})
			continue
		}

		// Mark as found and add to matched daemonsets
		foundDaemonSets[dsName] = true
		matchedDaemonSets = append(matchedDaemonSets, ds)
	}

	// Check if we have a pod store for this namespace
	podStore, ok := ic.podStores[namespace]
	if !ok {
		return nil, fmt.Errorf("no pod store found for namespace %s", namespace)
	}

	// For each matched daemonset, get pods
	for _, daemonSet := range matchedDaemonSets {
		// Get the label selector for this daemonset
		selector, err := metav1.LabelSelectorAsSelector(daemonSet.Spec.Selector)
		if err != nil {
			ic.logger.Error("Failed to parse daemonset selector", err, map[string]interface{}{
				"daemonset": daemonSet.Name,
				"namespace": namespace,
			})
			continue
		}

		// Find pods matching this selector
		pods := podStore.List()
		var matchingPods []corev1.Pod
		for _, obj := range pods {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				continue // Skip if not a pod
			}

			// Check if pod matches the selector (we already know it's in the right namespace)
			podLabels := labels.Set(pod.Labels)
			if selector.Matches(podLabels) {
				matchingPods = append(matchingPods, *pod)
			}
		}

		// Process each pod
		for _, pod := range matchingPods {
			modelPod := ic.convertPod(pod, "DaemonSet", daemonSet.Name)
			result = append(result, modelPod)
		}

		// Check if this daemonset has any pods
		if len(matchingPods) == 0 {
			ic.logger.Error("DaemonSet has 0 running pods", nil, map[string]interface{}{
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

	// Add missing daemonsets as missing pods
	for _, name := range missingDaemonSets {
		ic.logger.Warn("Missing daemonset", map[string]interface{}{
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

	return result, nil
}

// determineOwner extracts the kind and name of the workload that owns this pod
func (ic *InformerCollector) determineOwner(pod corev1.Pod) (string, string) {
	// Check for ownership references
	for _, owner := range pod.OwnerReferences {
		switch owner.Kind {
		case "ReplicaSet":
			// For ReplicaSet, need to determine if it's part of a Deployment
			// Look for the ReplicaSet in the cache
			replicaSets, err := ic.clientset.AppsV1().ReplicaSets(pod.Namespace).Get(
				context.Background(), owner.Name, metav1.GetOptions{})
			if err == nil {
				// Check if the ReplicaSet is owned by a Deployment
				for _, rsOwner := range replicaSets.OwnerReferences {
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
			job, err := ic.clientset.BatchV1().Jobs(pod.Namespace).Get(
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
	if len(pod.Name) > 0 && pod.Name[len(pod.Name)-1] >= '0' && pod.Name[len(pod.Name)-1] <= '9' {
		// This might be a StatefulSet pod (name-0, name-1, etc.)
		for i := len(pod.Name) - 1; i >= 0; i-- {
			if pod.Name[i] == '-' {
				statefulSetName := pod.Name[:i]
				return "StatefulSet", statefulSetName
			}
		}
	}

	// Default to standalone pod
	return "Pod", pod.Name
}

// determinePodStatus maps Kubernetes pod phase to our internal status
func (ic *InformerCollector) determinePodStatus(pod corev1.Pod) models.PodStatus {
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
func (ic *InformerCollector) calculatePodAge(startTime time.Time) string {
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
func (ic *InformerCollector) countRestarts(pod corev1.Pod) int {
	total := 0
	for _, containerStatus := range pod.Status.ContainerStatuses {
		total += int(containerStatus.RestartCount)
	}
	return total
}

// convertPod converts a Kubernetes Pod to our internal Pod model
func (ic *InformerCollector) convertPod(pod corev1.Pod, kind string, ownerName string) models.Pod {
	// Determine pod status
	status := ic.determinePodStatus(pod)

	// Calculate pod age
	age := ic.calculatePodAge(pod.CreationTimestamp.Time)

	// Count restarts
	restarts := ic.countRestarts(pod)

	// Get CPU and memory usage from the metrics collector
	cpu, memory, cpuValue, memoryValue, cpuTrend, memoryTrend := ic.metricsCollector.GetPodMetrics(pod.Namespace, pod.Name)

	// Process container statuses
	var containerStatuses []models.ContainerStatus
	totalContainers := len(pod.Spec.Containers)
	readyContainers := 0

	// Map to track which containers we've seen in the status
	seenContainers := make(map[string]bool)

	// Map to identify init containers (we'll exclude these from the UI visualization)
	initContainerMap := make(map[string]bool)
	for _, initContainer := range pod.Spec.InitContainers {
		initContainerMap[initContainer.Name] = true
	}

	// Process container statuses from the pod status
	for _, containerStatus := range pod.Status.ContainerStatuses {
		// Skip init containers for the UI visualization
		if initContainerMap[containerStatus.Name] {
			continue
		}

		seenContainers[containerStatus.Name] = true

		// Determine container state
		containerState := "unknown"
		reason := ""
		message := ""

		if containerStatus.State.Running != nil {
			containerState = "running"
		} else if containerStatus.State.Waiting != nil {
			containerState = "waiting"
			reason = containerStatus.State.Waiting.Reason
			message = containerStatus.State.Waiting.Message
		} else if containerStatus.State.Terminated != nil {
			containerState = "terminated"
			reason = containerStatus.State.Terminated.Reason
			message = containerStatus.State.Terminated.Message
		}

		// Add to container statuses
		containerStatuses = append(containerStatuses, models.ContainerStatus{
			Name:    containerStatus.Name,
			Ready:   containerStatus.Ready,
			Status:  containerState,
			Reason:  reason,
			Message: message,
		})

		// Count ready containers
		if containerStatus.Ready {
			readyContainers++
		}
	}

	// Add init containers to the total count for metrics purposes
	// but we won't display them in the UI
	totalContainers += len(pod.Spec.InitContainers)

	// Add entries for regular containers that don't have a status yet
	for _, container := range pod.Spec.Containers {
		if !seenContainers[container.Name] && !initContainerMap[container.Name] {
			containerStatuses = append(containerStatuses, models.ContainerStatus{
				Name:    container.Name,
				Ready:   false,
				Status:  "waiting",
				Reason:  "ContainerCreating",
				Message: "Container is being created",
			})
		}
	}

	return models.Pod{
		Name:              pod.Name,
		Status:            status,
		StartTime:         pod.CreationTimestamp.Time,
		Age:               age,
		Restarts:          restarts,
		CPU:               cpu,
		CPUValue:          cpuValue,
		CPUTrend:          cpuTrend,
		Memory:            memory,
		MemoryValue:       memoryValue,
		MemoryTrend:       memoryTrend,
		Kind:              kind, // Use the determined workload kind
		Namespace:         pod.Namespace,
		OwnerName:         ownerName, // Include the owner name for better identification
		ContainerStatuses: containerStatuses,
		TotalContainers:   totalContainers,
		ReadyContainers:   readyContainers,
	}
}
