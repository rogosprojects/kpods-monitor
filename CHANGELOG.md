# Changelog

All notable changes to the kpods-monitor project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
