# Changelog

All notable changes to the kpods-monitor project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.2] - 2025-04-24

### Added
- Added detailed debug logging to show the reason for client updates (e.g., server events)

## [0.2.1] - 2025-04-23

### Added
- Added container status visualization for Running pods to show individual container readiness (excluding init containers)

## [0.2.0] - 2025-04-23

### Added
- Implemented Kubernetes Informers/Watch pattern for real-time updates
- Enhanced WebSocket implementation for efficient client updates
- Added custom metrics collector for efficient CPU and memory metrics collection

### Changed
- Replaced polling mechanism with Kubernetes Informers for improved efficiency
- Removed manual refresh API endpoint in favor of real-time updates
- Updated client-side code to rely exclusively on WebSockets
- Consolidated metrics collection code to remove redundancy
- Added server-side sorting of pods by name for consistent display order
- Implemented debouncing mechanism to prevent race conditions with high-volume updates
- Increased update channel buffer size for better handling of concurrent updates

### Removed
- Removed polling-related configuration and code
- Removed startPolling function and related functionality
- Removed support for pod collection by labels and annotations

## [0.1.4] - 2025-04-16

### Security
- Fixed authentication bypass vulnerability where pressing ESC during the authentication prompt would still load the dashboard
- Ensured WebSocket connections are properly authenticated
- Added authentication verification before loading any dashboard content
- Improved error messages for authentication failures

## [0.1.3] - 2025-04-15

### Added
- Enhanced responsive design for iPhone devices
- Added new media queries for small mobile devices (max-width: 480px)
- Added medium-sized device breakpoint (max-width: 768px) for tablets
- Support for application version display

## [0.1.2]

### Added
- Improved resource efficiency by removing `startRefreshLoop` and relying solely on `startPolling`

### Changed
- Modified data refresh mechanism to only poll when clients are connected
- Added immediate data refresh when the first client connects
- Enhanced polling function to prevent excessive refreshes

## [0.1.1] - 2025-04-14

### Added
- Support for hosting the application and API under a custom base path (e.g., `/some-path`)
- Added updateDashboardName feature

## [0.1.0] - 2025-04-14

### Added
- Initial release of kpods-monitor
- Kubernetes pod monitoring dashboard
- Real-time updates via WebSocket
- Filtering by namespace
- Authentication support
- Responsive UI design
