# ![](./ui/public/logomark.png) Lightweight Kubernetes Pod Monitoring

![Go Version](https://img.shields.io/badge/golang-1.24+-blue)
![License](https://img.shields.io/badge/license-MIT-orange)

A modern dashboard for monitoring Kubernetes applications and their pods with real-time updates.

## Overview

This dashboard provides a real-time visualization of Kubernetes applications with comprehensive monitoring capabilities. It displays application health status, categorizing each application as Healthy, Warning, or Critical based on the state of its pods. You can monitor individual pod statuses such as Running, Pending, or CrashLoopBackOff, along with detailed pod information including age, restart count, and resource utilization. The dashboard tracks CPU and memory usage with trend indicators to help identify potential resource issues before they become critical.

The system excels at detecting configuration discrepancies by highlighting missing workloads that are defined in your configuration but not found in the cluster. It also identifies zero-pod workloads—those that exist in the cluster but have no running pods. All this information is logically organized and grouped by namespaces and application contexts, making it easy to monitor complex multi-namespace deployments.

## ✨ Key Features

- **Lightweight & Lightning Fast**: Uses minimal resources. Optimized Kubernetes API queries with efficient caching
- **Real-time Updates**: WebSocket-based live updates using Kubernetes Informers for improved efficiency
- **Namespace Filtering**: Filter applications by namespace
- **Resource Trend Indicators**: Visual indicators for increasing/decreasing resource usage
- **Zero Dependencies**: Runs as a single binary, no external database required
- **Security Features**: Rate limiting, connection limits, and proper authentication options
- **Multi-Resource Support**: Monitors Deployments, StatefulSets, DaemonSets


## 🚀 Quick Start

### Prerequisites

- Go 1.16+
- Access to a Kubernetes cluster
- Kubernetes metrics-server (optional, for resource metrics)

### Running locally

1. Clone the repository
   ```
   git clone https://github.com/rogosprojects/kpods-monitor.git
   cd kpods-monitor
   ```

2. Create a configuration file (see `config.yaml` for example)
   ```
   cp example-config.yaml config.yaml
   # Edit config.yaml with your preferred editor
   ```

3. Run the server:
   ```
   go run cmd/server/main.go --config=config.yaml
   ```

4. Open the dashboard in your browser at http://localhost:8080

### Building from source

Build the application:
```
./build.sh
```

Run the built binary:
```
./kpods-monitor -config config.yaml
```

### Running with Docker

Build the Docker image:
```
docker build -t kpods-monitor .
```

Run the container:
```
docker run -p 8080:8080 -v ~/.kube/config:/app/.kube/config -v $(pwd)/config.yaml:/app/config.yaml kpods-monitor
```

### Command-line Options

The application supports the following command-line options:

- `--config`: Path to configuration file (default: "config.yaml")
- `--version`: Show version and exit


## 📊 Dashboard Features

- **Application Health**: Instantly see healthy, warning, or critical status
- **Pod Details**: Status, age, restarts, CPU/memory with trend indicators
- **Container Status**: Visual indicators for container readiness within pods
- **Multi-Namespace View**: Group applications across namespaces
- **Missing Workload Detection**: Quickly identify configuration issues
- **Zero-Pod Workload Detection**: Find workloads without running pods
- **Resource Trends**: Visual indicators for increasing/decreasing resource usage

## 📄 Configuration

Configure via a simple YAML file:
```yaml
applications:
  - name: "Frontend App"
    selector:
      production:
        deployments: ["frontend-deployment"]
```

### Configuration Structure

The dashboard uses a namespace-specific configuration structure:

```yaml
applications:
  - name: "Frontend App"
    description: "Customer-facing web UI"
    order: 10  # Controls display order in UI (lower values appear first)
    selector:
      production:  # Namespace name
        deployments:
        - frontend-deployment
        - auth-deployment
      staging:  # Another namespace for the same application
        deployments:
        - frontend-staging
```

### Selector Options

For each namespace, you can use any of the following selection methods:

1. **Deployments**: Select pods by deployment name (must match exactly)
   ```yaml
   selector:
     production:
       deployments: ["frontend-deployment", "auth-deployment"]
   ```

2. **StatefulSets**: Select pods by statefulset name (must match exactly)
   ```yaml
   selector:
     production:
       statefulSets: ["database", "queue"]
   ```

3. **DaemonSets**: Select pods by daemonset name (must match exactly)
   ```yaml
   selector:
     production:
       daemonSets: ["logging-agent"]
   ```

**Important**:
1. Workload names (deployments, statefulSets, daemonSets) require exact matches. For example, specifying "api" will NOT match "api-gateway".
2. You can specify different workloads for different namespaces within the same application.
3. Missing workloads (defined in config but not found in cluster) will appear in light grey with "Unknown" status, and the application health will show a warning.

## Configuration Options

The dashboard supports various configuration options in the `config.yaml` file:

### General Configuration

```yaml
general:
  # Name of the dashboard instance
  name: "Kubernetes Pod Monitor"

  # Base path for hosting the application (e.g., "/some-path")
  basePath: ""

  # Port number for the dashboard server
  port: 8080

  # Enable or disable debug logging
  # When enabled, shows detailed information about client updates and their reasons
  debug: false

  # Base path for hosting the application (e.g., "/some-path")
  basePath: ""

  # Authentication configuration
  auth:
    enabled: false
    type: "none"  # "basic", "token", or "none"
    username: ""
    password: ""
    apiKey: ""
```

### Cluster Configuration

```yaml
cluster:
  # Use in-cluster config when running inside Kubernetes
  inCluster: false

  # Path to kubeconfig file (used when inCluster is false)
  kubeConfigPath: "~/.kube/config"
```



## 📱 UI Features

Modern, responsive interface that works on any device:
- Namespace filtering
- Quick health status indicators
- Resource usage with trend visualization
- Detailed pod metrics

## 🔒 Security Features

- Multiple authentication options
- Rate limiting
- Connection limiting
- Security headers
- RBAC-compatible

## 📦 Kubernetes Deployment

To deploy the dashboard in your Kubernetes cluster:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kpods-monitor
  namespace: monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kpods-monitor
  template:
    metadata:
      labels:
        app: kpods-monitor
    spec:
      containers:
      - name: kpods-monitor
        image: kpods-monitor:latest
        ports:
        - containerPort: 8080
        volumeMounts:
        - name: config
          mountPath: /app/config.yaml
          subPath: config.yaml
      volumes:
      - name: config
        configMap:
          name: kpods-monitor-config
---
apiVersion: v1
kind: Service
metadata:
  name: kpods-monitor
  namespace: monitoring
spec:
  selector:
    app: kpods-monitor
  ports:
  - port: 80
    targetPort: 8080
  type: ClusterIP
```

## Features Details

### Container Status Visualization

For pods in the "Running" state, the dashboard provides a detailed visualization of individual container statuses:

- Color-coded segments show the status of each container within the pod
- Green segments indicate ready containers
- Orange segments indicate running but not ready containers
- Red segments indicate containers in a waiting state (e.g., image pulling)
- Gray segments indicate terminated containers

This feature helps you quickly identify pods that are technically running but have containers that aren't ready yet, which can help diagnose issues with multi-container pods.

### Resource Usage Trend Indicators

The dashboard shows trend indicators for CPU and memory usage, helping you quickly identify resources that are increasing or decreasing significantly:

- ↑ (Red): Resource usage is trending upward
- ↓ (Green): Resource usage is trending downward
- No indicator shown for stable usage patterns

Small changes are considered "static" and don't trigger a trend indicator. This prevents noise from minor fluctuations.

### Missing Workload Detection

The dashboard identifies workloads that are defined in your configuration but not found in the cluster:

- Missing workloads appear in light grey with "Unknown" status
- Applications containing missing workloads show an orange "Warning" health status
- This helps you quickly identify deployment issues or configuration discrepancies

### Application Display Order

You can control the order of applications in the UI using the `order` parameter:

```yaml
applications:
  - name: "Critical App"
    order: 10  # Lower values appear first
    # ...
  - name: "Less Important App"
    order: 20  # Higher values appear later
    # ...
```

Applications without an order value are displayed alphabetically after those with order values.


### Base Path Configuration

The dashboard can be hosted under a custom base path, which is useful when deploying behind a reverse proxy or in environments where the application needs to be served from a subpath:

```yaml
general:
  # Base path for hosting the application
  basePath: "/kpods-monitor"
```

With this configuration:
- The dashboard will be accessible at `http://your-server/kpods-monitor`
- All API endpoints will be prefixed with the base path (e.g., `/kpods-monitor/api/applications`)
- WebSocket connections will use the correct path automatically
- Static assets will be served correctly under the base path

Leave the `basePath` empty to host at the root path.

### Authentication

The dashboard supports multiple authentication methods:

- **Basic Authentication**: Username and password authentication
- **Token Authentication**: API key-based authentication
- **No Authentication**: For development or secure internal environments

Authentication can be configured in the config.yaml file:

```yaml
general:
  auth:
    enabled: true
    type: "basic"  # or "token"
    username: "admin"
    password: "secure-password"
    # OR for token auth
    # type: "token"
    # apiKey: "your-api-key"
```

### Rate Limiting

The dashboard implements rate limiting to prevent abuse:

- Limits API requests per IP address
- Configurable rate limit thresholds
- Automatic cleanup of rate limit tracking data

### Connection Limits

To prevent resource exhaustion, the dashboard limits:

- Maximum concurrent WebSocket connections
- Maximum connections per IP address
- Automatic cleanup of inactive connections

### Security Headers

The dashboard sets appropriate security headers on all responses:

- Content Security Policy (CSP)
- X-Content-Type-Options
- X-Frame-Options
- Referrer-Policy
- Permissions-Policy


### Real-time Updates with Kubernetes Informers

The dashboard uses Kubernetes Informers/Watch pattern instead of polling for improved efficiency:

- Establishes a single connection to the Kubernetes API server
- Receives real-time updates when resources change
- Significantly reduces API server load compared to polling
- Provides immediate notification of pod status changes
- Uses WebSockets to push updates to clients in real-time
- Includes detailed debug logging showing the reason for client updates (when debug mode is enabled)

Read more about Informers here: [Demystifying Kubernetes Informers
](https://medium.com/@jeevanragula/demystifying-kubernetes-informer-streamlining-event-driven-workflows-955285166993)

### Debug Logging

When debug mode is enabled, the dashboard provides detailed logging about client updates:

- Shows the specific reason for each client update (e.g., "Pod added", "Deployment updated")
- Logs when updates are triggered by Kubernetes Informer events
- Logs when updates are triggered by metrics changes
- Logs when updates are triggered by client connections
- Helps diagnose when and why clients are receiving updates

### Metrics Collection

The dashboard collects pod metrics using the Kubernetes Metrics API:

- Automatically detects if metrics-server is available
- Collects CPU and memory usage for each pod in efficient batches
- Calculates usage trends based on historical data
- Formats metrics in human-readable format (e.g., MiB, GiB)

### Health Calculation

Application health is calculated based on pod statuses:

- **Critical**: Any pod in CrashLoopBackOff or Error state
- **Warning**: Any pod in Pending or Terminating state, or with high restart counts
- **Warning**: Any missing workloads or workloads with zero pods
- **Healthy**: All pods running normally

### Caching

The dashboard implements efficient caching:

- LRU (Least Recently Used) caching for application data
- Metrics caching for trend calculation
- Automatic cache cleanup to prevent memory leaks
## 🤝 Contributing

Contributions welcome!

## 📜 License

This project is licensed under the MIT License - see the LICENSE file for details.
