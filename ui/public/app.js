document.addEventListener('DOMContentLoaded', () => {
  // State
  let applications = [];
  let namespaces = [];
  let selectedNamespace = 'all';
  let isLoading = true;
  let lastUpdated = null;
  let dashboardInitialized = false;
  let configuredRefreshInterval = 30; // Default value, will be updated from server
  let dashboardName = 'Kubernetes Pod Monitor Dashboard';
  let wsConnection = null;
  let wsReconnectTimeout = null;
  let wsReconnectAttempts = 0;
  let wsPingInterval = null; // Interval for sending ping messages
  let version = '0.0.0';

  // Get the base path from the current URL
  // This handles cases where the app is hosted under a subpath
  const getBasePath = () => {
    // Get the path from the current URL, excluding any query parameters
    const path = window.location.pathname;

    // If the path ends with '/' or is empty, return it as is
    if (path === '/' || path === '') {
      return '';
    }

    // If the path ends with '/index.html', remove it
    if (path.endsWith('/index.html')) {
      return path.substring(0, path.length - 11); // 11 is the length of '/index.html'
    }

    // Otherwise, return the directory part of the path
    const lastSlashIndex = path.lastIndexOf('/');
    if (lastSlashIndex <= 0) {
      return '';
    }

    return path.substring(0, lastSlashIndex);
  };

  // Get the base path once at initialization
  const basePath = getBasePath();

  // DOM elements
  const rootElement = document.getElementById('root');

  // Initialize the application
  init();

  // Main initialization function
  async function init() {
    renderLoading();
    try {
      // Fetch initial configuration
      await fetchConfig();

      // Connect to WebSocket for live updates
      connectWebSocket();

      // Still fetch initial data via HTTP in case WebSocket connection is slow
      await fetchData(true);
    } catch (error) {
      console.error('Failed to initialize:', error);
      renderError('Failed to load dashboard data. Please try refreshing the page.');
    }
  }

  // Connect to WebSocket for real-time updates
  function connectWebSocket() {
    // Close existing connection if any
    if (wsConnection) {
      wsConnection.close();
    }

    // Clear any pending reconnection timeouts
    if (wsReconnectTimeout) {
      clearTimeout(wsReconnectTimeout);
    }

    // Determine WebSocket protocol (ws or wss) based on page protocol
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';

    // Use the same URL structure as regular API calls
    // The authentication is handled by the authMiddleware on the server
    const wsUrl = `${wsProtocol}//${window.location.host}${basePath}/api/ws`;

    wsConnection = new WebSocket(wsUrl);

    // Connection opened
    wsConnection.onopen = () => {
      console.log('WebSocket connection established');
      wsReconnectAttempts = 0; // Reset reconnect attempts counter
      updateConnectionStatus(true);

      // Start sending ping messages to keep the connection alive
      startPingInterval();
    };

    // Listen for messages
    wsConnection.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);

        // Handle pong response from server
        if (data.type === 'pong') {
          console.log('Received pong from server');
          return;
        }

        // Handle normal data updates
        applications = data.applications || [];
        lastUpdated = data.lastUpdated ? new Date(data.lastUpdated) : new Date();

        // Extract all unique namespaces
        const newNamespaces = extractNamespaces(applications);

        // Only rebuild namespace selector if namespaces changed
        if (!arraysEqual(namespaces, newNamespaces)) {
          namespaces = newNamespaces;
          if (dashboardInitialized) {
            updateNamespaceSelector();
          }
        }

        // Update the UI
        if (!dashboardInitialized) {
          isLoading = false;
          buildFullDashboard();
        } else {
          updateLastUpdatedTime();
          updateApplicationsData();
        }
      } catch (error) {
        console.error('Error processing WebSocket message:', error);
      }
    };

    // Connection closed
    wsConnection.onclose = (event) => {
      console.log(`WebSocket connection closed (${event.code}: ${event.reason})`);
      updateConnectionStatus(false);

      // Stop the ping interval when connection is closed
      stopPingInterval();

      // Attempt to reconnect with exponential backoff
      const backoffDelay = Math.min(1000 * Math.pow(1.5, wsReconnectAttempts), 30000);
      wsReconnectAttempts++;

      wsReconnectTimeout = setTimeout(() => {
        console.log(`Attempting to reconnect (attempt ${wsReconnectAttempts})...`);
        connectWebSocket();
      }, backoffDelay);
    };

    // Connection error
    wsConnection.onerror = (error) => {
      console.error('WebSocket error:', error);
      updateConnectionStatus(false);
    };
  }

  // Start sending ping messages to keep the connection alive
  function startPingInterval() {
    // Clear any existing interval
    stopPingInterval();

    // Send a ping every 60 seconds (well within the server's 120-second read timeout)
    wsPingInterval = setInterval(() => {
      if (wsConnection && wsConnection.readyState === WebSocket.OPEN) {
        console.log('Sending ping to keep WebSocket connection alive');
        // Send a simple ping message
        wsConnection.send(JSON.stringify({ type: 'ping' }));
      }
    }, 60000); // 60 seconds
  }

  // Stop the ping interval
  function stopPingInterval() {
    if (wsPingInterval) {
      clearInterval(wsPingInterval);
      wsPingInterval = null;
    }
  }

  // Extract namespaces from applications
  function extractNamespaces(apps) {
    const nsSet = new Set();
    apps.forEach(app => {
      if (app.namespaces) {
        app.namespaces.forEach(ns => nsSet.add(ns));
      }
    });
    return Array.from(nsSet).sort();
  }

  // Update connection status indicator
  function updateConnectionStatus(isConnected) {
    const statusEl = document.getElementById('connection-status');
    if (!statusEl) return;

    if (isConnected) {
      statusEl.className = 'connection-status connected';
      statusEl.title = 'Live connection established';
      statusEl.innerHTML = '● Live';
    } else {
      statusEl.className = 'connection-status disconnected';
      statusEl.title = 'Connection lost, attempting to reconnect...';
      statusEl.innerHTML = '● Reconnecting...';
    }
  }

  // Fetch data from the API (used as fallback and initial load)
  async function fetchData(isInitialLoad = false) {
    if (isInitialLoad) {
      isLoading = true;
    }

    try {
      // Simple HTTP fetch to get applications data
      const url = `${basePath}/api/applications`;

      // Fetch applications
      const response = await fetch(url);
      if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`);
      }
      const data = await response.json();
      const newApplications = data.applications || [];
      lastUpdated = data.lastUpdated ? new Date(data.lastUpdated) : new Date();

      // Fetch namespaces
      const nsResponse = await fetch(`${basePath}/api/namespaces`);
      if (nsResponse.ok) {
        const newNamespaces = await nsResponse.json();
        // Only rebuild namespace selector if namespaces changed
        if (!arraysEqual(namespaces, newNamespaces)) {
          namespaces = newNamespaces;
          if (dashboardInitialized) {
            updateNamespaceSelector();
          }
        }
      }

      // Update applications data
      applications = newApplications;

      // Initial load or rebuild needed
      if (isInitialLoad || !dashboardInitialized) {
        isLoading = false;
        buildFullDashboard();
      } else {
        // Just update pods data and last updated time
        updateLastUpdatedTime();
        updateApplicationsData();
      }

    } catch (error) {
      console.error('Error fetching data:', error);
      if (isInitialLoad) {
        isLoading = false;
        renderError('Failed to load data from the server.');
      } else {
        showToast('Failed to refresh data. Will try again later.');
      }
    }
  }

  // Fetch config from the server
  async function fetchConfig() {
    try {
      // Fetch refresh interval from server config
      const response = await fetch(`${basePath}/api/config`);
      if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`);
      }
      const config = await response.json();

      // Update global state with the server-provided interval
      configuredRefreshInterval = config.refreshInterval || 30;
      dashboardName = config.dashboardName || 'Kubernetes Pod Monitor Dashboard';
      version = config.version || '0.0.0';

      // Update displayed refresh text if dashboard is already visible
      if (dashboardInitialized) {
        updateRefreshIntervalText(configuredRefreshInterval);
        updateDashboardName(dashboardName);
      }
    } catch (error) {
      console.error('Error fetching config:', error);
      // Fallback to 30 seconds if config fetch fails
    }
  }

  // Update the dashboard name displayed on the dashboard
  function updateDashboardName(name) {
    dashboardName = name; // Update the global variable
    const nameEl = document.querySelector('.dashboard-description strong');
    if (nameEl) {
      nameEl.textContent = name;
    }
  }

  // Helper function to check if arrays are equal
  function arraysEqual(a, b) {
    if (a === b) return true;
    if (a == null || b == null) return false;
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) {
      if (a[i] !== b[i]) return false;
    }
    return true;
  }

  // Update the refresh interval text displayed on the dashboard
  function updateRefreshIntervalText(seconds) {
    configuredRefreshInterval = seconds; // Update the global variable
    const descriptionEl = document.querySelector('.dashboard-description small');
    if (descriptionEl) {
      descriptionEl.textContent = `Data automatically refreshes every ${seconds} seconds with an active connection. Filter by namespace using the dropdown above.`;
    }
  }

  // Get the current refresh interval for display
  function getRefreshIntervalText() {
    return configuredRefreshInterval;
  }

  // Build and display a temporary toast notification
  function showToast(message) {
    // Create toast element if it doesn't exist
    let toast = document.getElementById('toast-notification');
    if (!toast) {
      toast = document.createElement('div');
      toast.id = 'toast-notification';
      toast.className = 'toast-notification';
      document.body.appendChild(toast);
    }

    // Set message and show
    toast.textContent = message;
    toast.classList.add('show');

    // Hide after 3 seconds
    setTimeout(() => {
      toast.classList.remove('show');
    }, 3000);
  }

  // Render the loading state
  function renderLoading() {
    rootElement.innerHTML = `
      <div class="dashboard">
        <header class="dashboard-header">
          <h1>
            <img src="${basePath}/logo.png" alt="Pod Monitoring Dashboard Logo" class="dashboard-logo"><span class="app-version">v${version}</span>
          </h1>
          <div class="dashboard-actions">
            <select class="namespace-selector" disabled>
              <option value="all">All Namespaces</option>
            </select>
          </div>
        </header>
        <div class="loading">
          <div class="loading-spinner"></div>
          <span>Loading dashboard data...</span>
        </div>
      </div>
    `;
  }

  // Render an error message
  function renderError(message) {
    rootElement.innerHTML = `
      <div class="dashboard">
        <header class="dashboard-header">
          <h1>
            <img src="${basePath}/logo.png" alt="Pod Monitoring Dashboard Logo" class="dashboard-logo"><span class="app-version">v${version}</span>
          </h1>
          <div class="dashboard-actions">
            <select class="namespace-selector" disabled>
              <option value="all">All Namespaces</option>
            </select>
          </div>
        </header>
        <div class="empty-state">
          <h3>Error</h3>
          <p>${message}</p>
        </div>
      </div>
    `;
  }

  // Build the complete dashboard (used only on first load or major changes)
  function buildFullDashboard() {
    // If loading, show loading state
    if (isLoading) {
      renderLoading();
      return;
    }

    // Filter applications by selected namespace
    const filteredApps = getFilteredApplications();

    // Format last updated time
    const formattedTime = lastUpdated ? new Date(lastUpdated).toLocaleTimeString() : '';

    // Generate HTML for applications
    let appsHtml = '';
    if (filteredApps.length === 0) {
      appsHtml = `
        <div class="empty-state">
          <h3>No applications found</h3>
          <p>No applications match the current filter criteria.</p>
        </div>
      `;
    } else {
      appsHtml = `
        <div class="applications-container">
          ${filteredApps.map(app => renderApplicationCard(app)).join('')}
        </div>
      `;
    }

    // Render the entire dashboard
    rootElement.innerHTML = `
      <div class="dashboard">
        <header class="dashboard-header">
          <h1>
            <img src="${basePath}/logo.png" alt="Pod Monitoring Dashboard Logo" class="dashboard-logo"><span class="app-version">v${version}</span>
          </h1>
          <div class="dashboard-actions">
            <select class="namespace-selector" id="namespace-selector">
              <option value="all">All Namespaces</option>
              ${namespaces.map(ns => `
                <option value="${ns}" ${selectedNamespace === ns ? 'selected' : ''}>${ns}</option>
              `).join('')}
            </select>
            <div id="connection-status" class="connection-status">● Connecting...</div>
          </div>
        </header>

        <div class="dashboard-info">
          <div class="dashboard-description">
            <strong>${dashboardName}</strong> - Real-time view of your pod statuses and health across applications.
            <br>
            <small>Data automatically refreshes every ${getRefreshIntervalText()} seconds with an active connection. Filter by namespace using the dropdown above.</small>
          </div>
          <div class="last-updated" id="last-updated">
            <span class="refresh-icon">⟳</span>
            Last updated: ${formattedTime}
          </div>
        </div>

        <div id="applications-area">
          ${appsHtml}
        </div>
      </div>
    `;

    // Add event listeners
    document.getElementById('namespace-selector').addEventListener('change', handleNamespaceChange);

    // Mark dashboard as initialized
    dashboardInitialized = true;

    // Update connection status based on WebSocket state
    updateConnectionStatus(wsConnection && wsConnection.readyState === WebSocket.OPEN);
  }

  // Render a single application card
  function renderApplicationCard(app) {
    // Calculate pod statistics
    const totalPods = app.pods ? app.pods.length : 0;
    const runningPods = app.pods ? app.pods.filter(pod => pod.status === "Running").length : 0;
    const podStats = totalPods > 0 ? `${runningPods}/${totalPods} running` : '0 pods';

    // Get namespaces
    const namespacesList = app.namespaces || [];

    const namespacesTags = namespacesList.map(ns =>
      `<span class="namespace-tag">${ns}</span>`
    ).join('');

    // Generate a unique ID for this application card
    const appId = `app-${app.name.replace(/[^a-zA-Z0-9]/g, '-').toLowerCase()}`;

    return `
      <div class="application-card" id="${appId}">
        <div class="application-header">
          <h2>${app.name}</h2>
          <div class="namespace-tags-container">
            ${namespacesTags}
          </div>
          <span class="pod-status-badge">${podStats}</span>
          <span class="health-indicator" style="background-color: ${getHealthColor(app.health)}">
            ${app.health}
          </span>
        </div>

        <div class="app-content">
          <div class="app-stats">
            ${app.description ? `<div class="app-description">${app.description}</div>` : '<div class="app-description-placeholder"></div>'}
          </div>

          <div class="pods-table-container">
            ${app.pods && app.pods.length > 0 ? `
              <table class="pods-table">
                <thead>
                  <tr>
                    <th>Pod Name</th>
                    <th>Status</th>
                    <th>Age</th>
                    <th>Restarts</th>
                    <th>CPU</th>
                    <th>Memory</th>
                  </tr>
                </thead>
                <tbody>
                  ${app.pods.map(pod => renderPodRow(pod)).join('')}
                </tbody>
              </table>
            ` : `
              <div class="empty-state">
                <p>No pods found for this application.</p>
              </div>
            `}
          </div>
        </div>
      </div>
    `;
  }

  // Get trend indicator symbol
  function getTrendIndicator(trend) {
    switch(trend) {
      case "up": return "↑";
      case "down": return "↓";
      case "static": return "→";
      default: return "";
    }
  }

  // Get CSS class for trend
  function getTrendClass(trend) {
    return trend ? `trend-${trend}` : '';
  }

  // Render a single pod table row
  function renderPodRow(pod) {
    // Add missing class for workloads that don't exist in the cluster
    const missingClass = pod.missing ? 'missing-workload' : '';

    // Format restarts with visual indicator
    let restartClass = 'restarts-ok';
    let restartIcons = '';

    if (pod.restarts > 0) {
      if (pod.restarts <= 2) {
        restartClass = 'restarts-warning';
        for (let i = 0; i < pod.restarts; i++) {
          restartIcons += '⟳ ';
        }
      } else if (pod.restarts <= 5) {
        restartClass = 'restarts-alert';
        for (let i = 0; i < Math.min(pod.restarts, 5); i++) {
          restartIcons += '⟳ ';
        }
      } else {
        restartClass = 'restarts-critical';
        restartIcons = '⟳ × ' + pod.restarts;
      }
    }

    // Format CPU with trend indicator - only show for up or down trends
    const cpuWithTrend = pod.cpu !== 'N/A' && pod.cpuTrend && pod.cpuTrend !== 'static'
      ? `${pod.cpu} <span class="trend-indicator ${getTrendClass(pod.cpuTrend)}" title="CPU usage trend">${getTrendIndicator(pod.cpuTrend)}</span>`
      : pod.cpu || 'N/A';

    // Format Memory with trend indicator - only show for up or down trends
    const memoryWithTrend = pod.memory !== 'N/A' && pod.memoryTrend && pod.memoryTrend !== 'static'
      ? `${pod.memory} <span class="trend-indicator ${getTrendClass(pod.memoryTrend)}" title="Memory usage trend">${getTrendIndicator(pod.memoryTrend)}</span>`
      : pod.memory || 'N/A';

    return `
      <tr class="${missingClass}">
        <td class="pod-name">${pod.name}</td>
        <td>
          <span class="status-indicator" style="background-color: ${getStatusColor(pod.status)}">
            ${pod.status}
          </span>
        </td>
        <td>${pod.age || 'N/A'}</td>
        <td class="${restartClass}">
          <span class="restart-count">${restartIcons || '0'}</span>
        </td>
        <td>${cpuWithTrend}</td>
        <td>${memoryWithTrend}</td>
      </tr>
    `;
  }

  // Handle namespace selector change
  function handleNamespaceChange(event) {
    selectedNamespace = event.target.value;
    // Only update the applications section, not the entire dashboard
    updateApplicationsSection();
  }

  // Get filtered applications based on selected namespace and sort by order
  function getFilteredApplications() {
    // First filter by namespace if needed
    const filtered = selectedNamespace === 'all'
      ? applications
      : applications.filter(app => app.namespaces && app.namespaces.includes(selectedNamespace));

    // Then sort by order (lower order values first)
    return filtered.sort((a, b) => {
      // Use order if both have it
      if (a.order !== undefined && b.order !== undefined) {
        return a.order - b.order;
      }
      // Items with order come before items without order
      if (a.order !== undefined) return -1;
      if (b.order !== undefined) return 1;
      // If neither has order, sort alphabetically
      return a.name.localeCompare(b.name);
    });
  }

  // Update only the applications section
  function updateApplicationsSection() {
    const filteredApps = getFilteredApplications();
    const applicationsArea = document.getElementById('applications-area');

    if (!applicationsArea) return;

    let appsHtml = '';
    if (filteredApps.length === 0) {
      appsHtml = `
        <div class="empty-state">
          <h3>No applications found</h3>
          <p>No applications match the current filter criteria.</p>
        </div>
      `;
    } else {
      appsHtml = `
        <div class="applications-container">
          ${filteredApps.map(app => renderApplicationCard(app)).join('')}
        </div>
      `;
    }

    applicationsArea.innerHTML = appsHtml;
  }

  // Update only the last updated time display
  function updateLastUpdatedTime() {
    const lastUpdatedElement = document.getElementById('last-updated');
    if (lastUpdatedElement) {
      const formattedTime = lastUpdated ? new Date(lastUpdated).toLocaleTimeString() : '';
      lastUpdatedElement.innerHTML = `<span class="refresh-icon">⟳</span> Last updated: ${formattedTime}`;
    }
  }

  // Update the namespace selector dropdown
  function updateNamespaceSelector() {
    const selector = document.getElementById('namespace-selector');
    if (!selector) return;

    // Save current selection
    const currentSelection = selector.value;

    // Update options
    selector.innerHTML = `
      <option value="all">All Namespaces</option>
      ${namespaces.map(ns => `
        <option value="${ns}" ${currentSelection === ns ? 'selected' : ''}>${ns}</option>
      `).join('')}
    `;
  }

  // Update application data without full page reload
  function updateApplicationsData() {
    const container = document.querySelector('.applications-container');
    if (!container) {
      // If container doesn't exist, we need to rebuild the applications section
      updateApplicationsSection();
      return;
    }

    const filteredApps = getFilteredApplications();

    // Get all existing app cards
    const existingAppCards = Array.from(document.querySelectorAll('.application-card'));
    const existingAppIds = existingAppCards.map(card => card.id);

    // Track new app IDs for comparison
    const newAppIds = filteredApps.map(app => `app-${app.name.replace(/[^a-zA-Z0-9]/g, '-').toLowerCase()}`);

    // 1. Update existing applications
    filteredApps.forEach(app => {
      const appId = `app-${app.name.replace(/[^a-zA-Z0-9]/g, '-').toLowerCase()}`;
      const existingCard = document.getElementById(appId);

      if (existingCard) {
        // Update the card with new data
        updateAppCard(existingCard, app);
      } else if (!existingAppIds.includes(appId)) {
        // Add new application card
        const newCardHtml = renderApplicationCard(app);
        const tempDiv = document.createElement('div');
        tempDiv.innerHTML = newCardHtml.trim();
        container.appendChild(tempDiv.firstChild);
      }
    });

    // 2. Remove applications that no longer exist
    existingAppCards.forEach(card => {
      if (!newAppIds.includes(card.id)) {
        card.remove();
      }
    });

    // 3. Handle empty state
    if (filteredApps.length === 0) {
      const applicationsArea = document.getElementById('applications-area');
      if (applicationsArea) {
        applicationsArea.innerHTML = `
          <div class="empty-state">
            <h3>No applications found</h3>
            <p>No applications match the current filter criteria.</p>
          </div>
        `;
      }
    }
  }

  // Update a specific application card with new data
  function updateAppCard(cardElement, app) {
    // Update health indicator
    const healthIndicator = cardElement.querySelector('.health-indicator');
    if (healthIndicator) {
      healthIndicator.style.backgroundColor = getHealthColor(app.health);
      healthIndicator.textContent = app.health;
    }

    // Calculate pod statistics
    const totalPods = app.pods ? app.pods.length : 0;
    const runningPods = app.pods ? app.pods.filter(pod => pod.status === "Running").length : 0;
    const podStats = totalPods > 0 ? `${runningPods}/${totalPods} running` : '0 pods';

    // Update pod stats badge
    const podBadge = cardElement.querySelector('.pod-status-badge');
    if (podBadge) {
      podBadge.textContent = podStats;
    }

    // Update namespaces tags
    const namespacesList = app.namespaces || [];
    const namespaceTags = namespacesList.map(ns =>
      `<span class="namespace-tag">${ns}</span>`
    ).join('');

    const tagsContainer = cardElement.querySelector('.namespace-tags-container');
    if (tagsContainer) {
      tagsContainer.innerHTML = namespaceTags;
    }

    // Update pods table
    const tableContainer = cardElement.querySelector('.pods-table-container');
    if (tableContainer) {
      if (app.pods && app.pods.length > 0) {
        tableContainer.innerHTML = `
          <table class="pods-table">
            <thead>
              <tr>
                <th>Pod Name</th>
                <th>Status</th>
                <th>Age</th>
                <th>Restarts</th>
                <th>CPU</th>
                <th>Memory</th>
              </tr>
            </thead>
            <tbody>
              ${app.pods.map(pod => renderPodRow(pod)).join('')}
            </tbody>
          </table>
        `;
      } else {
        tableContainer.innerHTML = `
          <div class="empty-state">
            <p>No pods found for this application.</p>
          </div>
        `;
      }
    }
  }

  // Get color for pod status
  function getStatusColor(status) {
    switch(status) {
      case "Running": return "green";
      case "CrashLoopBackOff": return "red";
      case "Error": return "red";
      case "Pending": return "orange";
      case "Terminating": return "orange";
      case "Completed": return "blue";
      case "Unknown": return "#cbd5e0"; // Light grey for unknown/missing workloads
      default: return "grey";
    }
  }

  // Get color for application health
  function getHealthColor(health) {
    switch(health) {
      case "Healthy": return "green";
      case "Warning": return "orange";
      case "Critical": return "red";
      default: return "grey";
    }
  }
});