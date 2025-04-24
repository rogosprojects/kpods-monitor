document.addEventListener('DOMContentLoaded', () => {
  // State
  let applications = [];
  let namespaces = [];
  let selectedNamespace = 'all';
  let isLoading = true;
  let lastUpdated = null;
  let dashboardInitialized = false;
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
      // First check authentication by making a request to a protected endpoint
      const authCheckResponse = await fetch(`${basePath}/api/config`);

      // If we get a 401 Unauthorized, show an authentication error
      if (authCheckResponse.status === 401) {
        renderError('Authentication required. Please refresh the page and enter valid credentials when prompted. Do not press ESC when the authentication dialog appears.');
        return;
      }

      // If we get any other error, show a general error
      if (!authCheckResponse.ok) {
        throw new Error(`HTTP error! status: ${authCheckResponse.status}`);
      }

      // Authentication successful, proceed with initialization
      // Fetch initial configuration
      const config = await authCheckResponse.json();
      // Update global state with the server-provided configuration
      dashboardName = config.dashboardName || 'Kubernetes Pod Monitor Dashboard';
      version = config.version || '0.0.0';

      // Update config display if dashboard is already initialized
      updateConfig();

      // Connect to WebSocket for live updates
      connectWebSocket();

      // No need to fetch initial data via HTTP as we'll get it from WebSocket
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

          // Show notification for the update if dashboard is initialized
          if (dashboardInitialized) {
            // Extract update reason from the data if available
            const updateReason = data.updateReason || '';

            // Only show notifications for pod/container status changes, not metrics updates
            if (updateReason && !updateReason.toLowerCase().includes('metrics')) {
              // Check if the update includes a specific pod
              if (updateReason.includes('pod:')) {
                // Extract pod name from the update reason
                const podMatch = updateReason.match(/pod:\s*([^\s]+)/);
                if (podMatch && podMatch[1]) {
                  const podName = podMatch[1];

                  // Determine notification type based on content
                  let notificationType = 'info';
                  let notificationDuration = 2500;

                  // Check for different types of status changes
                  if (updateReason.toLowerCase().includes('error') ||
                      updateReason.toLowerCase().includes('fail')) {
                    notificationType = 'error';
                    notificationDuration = 4000; // Show errors longer
                  } else if (updateReason.toLowerCase().includes('warn')) {
                    notificationType = 'warning';
                    notificationDuration = 3000;
                  } else if (updateReason.toLowerCase().includes('created')) {
                    notificationType = 'success';
                  } else if (updateReason.toLowerCase().includes('deleted') ||
                             updateReason.toLowerCase().includes('terminating')) {
                    notificationType = 'warning';
                  } else if (updateReason.toLowerCase().includes('crash') ||
                             updateReason.toLowerCase().includes('backoff')) {
                    notificationType = 'critical';
                    notificationDuration = 4000;
                  } else if (updateReason.toLowerCase().includes('security')) {
                    notificationType = 'security';
                    notificationDuration = 4000;
                  } else if (updateReason.toLowerCase().includes('network')) {
                    notificationType = 'network';
                  } else if (updateReason.toLowerCase().includes('system')) {
                    notificationType = 'system';
                  }

                  // Check if this is a status change notification
                  if (updateReason.toLowerCase().includes('status') ||
                      updateReason.toLowerCase().includes('created') ||
                      updateReason.toLowerCase().includes('deleted') ||
                      updateReason.toLowerCase().includes('updated')) {
                    showToast(`Pod ${podName} status changed`, notificationType, notificationDuration);
                  }
                }
              } else if (updateReason.toLowerCase().includes('status') ||
                         updateReason.toLowerCase().includes('created') ||
                         updateReason.toLowerCase().includes('deleted') ||
                         updateReason.toLowerCase().includes('updated')) {
                // Determine notification type based on content
                let notificationType = 'info';
                let notificationDuration = 2500;

                // Check for different types of status changes
                if (updateReason.toLowerCase().includes('error') ||
                    updateReason.toLowerCase().includes('fail')) {
                  notificationType = 'error';
                  notificationDuration = 4000; // Show errors longer
                } else if (updateReason.toLowerCase().includes('warn')) {
                  notificationType = 'warning';
                  notificationDuration = 3000;
                } else if (updateReason.toLowerCase().includes('created')) {
                  notificationType = 'success';
                } else if (updateReason.toLowerCase().includes('deleted') ||
                           updateReason.toLowerCase().includes('terminating')) {
                  notificationType = 'warning';
                } else if (updateReason.toLowerCase().includes('crash') ||
                           updateReason.toLowerCase().includes('backoff')) {
                  notificationType = 'critical';
                  notificationDuration = 4000;
                } else if (updateReason.toLowerCase().includes('security')) {
                  notificationType = 'security';
                  notificationDuration = 4000;
                } else if (updateReason.toLowerCase().includes('network')) {
                  notificationType = 'network';
                } else if (updateReason.toLowerCase().includes('system')) {
                  notificationType = 'system';
                }

                // Show a notification for other status changes
                showToast(`${updateReason}`, notificationType, notificationDuration);
              }
            }
          }
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

      // If the dashboard isn't initialized yet, this could be an authentication error
      if (!dashboardInitialized) {
        renderError('Failed to establish connection. This may be due to an authentication issue. Please refresh the page and enter valid credentials when prompted.');
      }
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

  // This function has been removed as we now rely exclusively on WebSockets for data updates

  // Update config display
  function updateConfig() {
    // Update displayed dashboard name if dashboard is already visible
    if (dashboardInitialized) {
      updateDashboardName(dashboardName);
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

  // Update the description text displayed on the dashboard
  function updateDescriptionText() {
    const descriptionEl = document.querySelector('.dashboard-description small');
    if (descriptionEl) {
      descriptionEl.textContent = `Data updates in real-time with an active connection. Filter by namespace using the dropdown above.`;
    }
  }

  // Call this function when building the dashboard
  function buildDashboardDescription() {
    updateDescriptionText();
  }

  // Enhanced notification manager with history and grouping
  const notificationManager = {
    container: null,
    maxToasts: 3,
    toasts: [],
    history: [],
    maxHistory: 50,
    historyPanel: null,
    historyToggle: null,
    unreadCount: 0,
    groupedNotifications: {}, // For tracking grouped notifications
    initialized: false,

    // Initialize the notification system immediately at page load
    initializeOnLoad() {
      // Only initialize once
      if (this.initialized) return;

      // Create toast container
      this.container = document.createElement('div');
      this.container.className = 'toast-container';
      document.body.appendChild(this.container);

      // Create history toggle button (bell icon)
      this.historyToggle = document.createElement('div');
      this.historyToggle.className = 'notification-history-toggle';
      this.historyToggle.innerHTML = `
        <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9"></path>
          <path d="M13.73 21a2 2 0 0 1-3.46 0"></path>
        </svg>
        <span class="badge" style="display: none;">0</span>
      `;

      // Add click event to toggle history panel
      this.historyToggle.addEventListener('click', (e) => {
        e.stopPropagation(); // Prevent clicks from propagating to document
        this.toggleHistoryPanel();
      });

      // Create history panel
      this.historyPanel = document.createElement('div');
      this.historyPanel.className = 'notification-history-panel';
      this.historyPanel.innerHTML = `
        <div class="notification-history-header">
          <div class="notification-history-title">Notification History</div>
          <div class="notification-history-actions">
            <button class="notification-history-action clear-all">Clear All</button>
            <button class="notification-history-action mark-read">Mark All Read</button>
          </div>
        </div>
        <div class="notification-history-list"></div>
      `;

      // Create a wrapper for the bell and panel to maintain proper positioning
      this.notificationWrapper = document.createElement('div');
      this.notificationWrapper.className = 'notification-wrapper';
      this.notificationWrapper.style.position = 'relative';
      this.notificationWrapper.style.display = 'flex';
      this.notificationWrapper.style.alignItems = 'center';
      this.notificationWrapper.appendChild(this.historyToggle);
      this.notificationWrapper.appendChild(this.historyPanel);

      // Add the bell to the header
      this.addBellToHeader();

      // Add event listeners for actions
      const clearAllBtn = this.historyPanel.querySelector('.clear-all');
      if (clearAllBtn) {
        clearAllBtn.addEventListener('click', () => {
          this.clearHistory();
        });
      }

      const markReadBtn = this.historyPanel.querySelector('.mark-read');
      if (markReadBtn) {
        markReadBtn.addEventListener('click', () => {
          this.markAllAsRead();
        });
      }

      // Close panel when clicking outside
      document.addEventListener('click', (event) => {
        if (!this.historyPanel.contains(event.target) &&
            !this.historyToggle.contains(event.target) &&
            this.historyPanel.classList.contains('show')) {
          this.toggleHistoryPanel(false);
        }
      });

      // Load history from localStorage if available
      this.loadHistory();

      // Mark as initialized
      this.initialized = true;
    },

    // Initialize the notification system if needed
    init() {
      if (!this.initialized) {
        this.initializeOnLoad();
      }
    },

    // Toggle the history panel
    toggleHistoryPanel(show) {
      if (show === undefined) {
        show = !this.historyPanel.classList.contains('show');
      }

      if (show) {
        this.historyPanel.classList.add('show');
        this.renderHistory();
        this.markAllAsRead();
      } else {
        this.historyPanel.classList.remove('show');
      }
    },

    // Render the notification history
    renderHistory() {
      const listEl = this.historyPanel.querySelector('.notification-history-list');
      if (!listEl) return;

      // Clear the list
      listEl.innerHTML = '';

      // Show empty message if no history
      if (this.history.length === 0) {
        listEl.innerHTML = '<div class="notification-history-empty">No notifications yet</div>';
        return;
      }

      // Add history items
      for (const item of this.history) {
        const historyItem = document.createElement('div');
        historyItem.className = 'notification-history-item';

        // Create icon based on severity/type
        let icon = '';
        switch (item.type) {
          case 'info': icon = 'ℹ️'; break;
          case 'success': icon = '✅'; break;
          case 'warning': icon = '⚠️'; break;
          case 'error': icon = '❌'; break;
          case 'critical': icon = '🔴'; break;
          case 'system': icon = '🔧'; break;
          case 'network': icon = '🌐'; break;
          case 'security': icon = '🔒'; break;
        }

        // Format time
        const time = this.formatTime(new Date(item.timestamp));

        // Add group count if this is a grouped notification
        const groupCountHtml = item.count > 1
          ? `<span class="notification-group-count">${item.count}</span>`
          : '';

        historyItem.innerHTML = `
          <span class="toast-icon">${icon}</span>
          <span class="notification-content">${item.message}${groupCountHtml}</span>
          <span class="notification-time">${time}</span>
        `;

        listEl.appendChild(historyItem);
      }
    },

    // Format time for display
    formatTime(date) {
      const now = new Date();
      const diffMs = now - date;
      const diffSec = Math.floor(diffMs / 1000);
      const diffMin = Math.floor(diffSec / 60);
      const diffHour = Math.floor(diffMin / 60);

      if (diffSec < 60) {
        return 'just now';
      } else if (diffMin < 60) {
        return `${diffMin}m ago`;
      } else if (diffHour < 24) {
        return `${diffHour}h ago`;
      } else {
        return date.toLocaleString();
      }
    },

    // Clear all history
    clearHistory() {
      this.history = [];
      this.saveHistory();
      this.renderHistory();
      this.updateUnreadCount(0);
    },

    // Mark all notifications as read
    markAllAsRead() {
      this.updateUnreadCount(0);
    },

    // Update the unread count badge
    updateUnreadCount(count) {
      if (count !== undefined) {
        this.unreadCount = count;
      }

      const badge = this.historyToggle.querySelector('.badge');
      if (badge) {
        badge.textContent = this.unreadCount;
        badge.style.display = this.unreadCount > 0 ? 'flex' : 'none';
      }
    },

    // Save history to localStorage
    saveHistory() {
      try {
        localStorage.setItem('notification_history', JSON.stringify(this.history));
      } catch (e) {
        console.error('Failed to save notification history:', e);
      }
    },

    // Load history from localStorage
    loadHistory() {
      try {
        const saved = localStorage.getItem('notification_history');
        if (saved) {
          this.history = JSON.parse(saved);
        }
      } catch (e) {
        console.error('Failed to load notification history:', e);
        this.history = [];
      }
    },

    // Add the bell icon to the header
    addBellToHeader() {
      // Function to try adding the bell to the header
      const tryAddingBell = () => {
        // Look for the notification bell container
        const bellContainer = document.getElementById('notification-bell-container');
        if (bellContainer) {
          // Clear any existing content
          bellContainer.innerHTML = '';
          // Add the notification wrapper to the container
          bellContainer.appendChild(this.notificationWrapper);
          return true;
        }
        return false;
      };

      // Try immediately (might work if the dashboard is already built)
      if (tryAddingBell()) {
        return;
      }

      // If not successful, set up a mutation observer to watch for DOM changes
      const observer = new MutationObserver(() => {
        if (tryAddingBell()) {
          // Once we've successfully added the bell, disconnect the observer
          observer.disconnect();
        }
      });

      // Start observing the document body for changes
      observer.observe(document.body, {
        childList: true,
        subtree: true
      });

      // Also set a timeout to retry a few times, in case the mutation observer doesn't catch it
      let attempts = 0;
      const maxAttempts = 10;
      const retryInterval = setInterval(() => {
        if (tryAddingBell() || ++attempts >= maxAttempts) {
          clearInterval(retryInterval);
        }
      }, 500);
    },

    // Check if a notification should be grouped with existing ones
    shouldGroup(message, type) {
      // Simple grouping logic: group by message prefix (first 15 chars) and type
      const prefix = message.substring(0, 15);
      const key = `${prefix}:${type}`;

      // Check if we have a recent notification with the same prefix
      const existingGroup = this.groupedNotifications[key];
      if (existingGroup) {
        const now = new Date();
        // Group if the last notification was less than 30 seconds ago
        if ((now - existingGroup.lastUpdate) < 30000) {
          return key;
        }
      }

      return null;
    },

    // Update a grouped notification
    updateGroupedNotification(groupKey, message) {
      const group = this.groupedNotifications[groupKey];
      if (!group) return null;

      // Update the group
      group.count++;
      group.lastUpdate = new Date();

      // Update the toast if it exists
      if (group.toast && group.toast.parentNode) {
        const countEl = group.toast.querySelector('.notification-group-count');
        if (countEl) {
          countEl.textContent = group.count;
        } else {
          const contentEl = group.toast.querySelector('.toast-content');
          if (contentEl) {
            contentEl.innerHTML = `${group.message} <span class="notification-group-count">${group.count}</span>`;
          }
        }
      }

      // Update the history item
      for (let i = 0; i < this.history.length; i++) {
        if (this.history[i].id === group.id) {
          this.history[i].count = group.count;
          this.history[i].message = message; // Update with latest message
          this.saveHistory();
          break;
        }
      }

      return group.toast;
    },

    // Show a toast notification
    show(message, type = 'info', duration = 3000) {
      this.init();

      // Check if this notification should be grouped
      const groupKey = this.shouldGroup(message, type);
      if (groupKey) {
        const existingToast = this.updateGroupedNotification(groupKey, message);
        if (existingToast) {
          // Reset the auto-dismiss timer
          if (existingToast.dismissTimer) {
            clearTimeout(existingToast.dismissTimer);
          }

          if (duration > 0) {
            existingToast.dismissTimer = setTimeout(() => {
              this.dismiss(existingToast);
            }, duration);
          }

          return existingToast;
        }
      }

      // Create a new notification ID
      const notificationId = Date.now().toString();

      // Create toast element
      const toast = document.createElement('div');
      toast.className = `toast-notification ${type}`;
      toast.dataset.id = notificationId;

      // Create toast content with severity-based icons
      let icon = '';
      switch (type) {
        case 'info': icon = 'ℹ️'; break;
        case 'success': icon = '✅'; break;
        case 'warning': icon = '⚠️'; break;
        case 'error': icon = '❌'; break;
        case 'critical': icon = '🔴'; break;
        case 'system': icon = '🔧'; break;
        case 'network': icon = '🌐'; break;
        case 'security': icon = '🔒'; break;
      }

      toast.innerHTML = `
        <span class="toast-icon">${icon}</span>
        <span class="toast-content">${message}</span>
        <span class="toast-close">×</span>
      `;

      // Add to container
      this.container.appendChild(toast);

      // Add to tracking array
      this.toasts.push(toast);

      // If this is a new group, track it
      if (groupKey) {
        this.groupedNotifications[groupKey] = {
          id: notificationId,
          message: message,
          type: type,
          count: 1,
          lastUpdate: new Date(),
          toast: toast
        };
      }

      // Add to history
      const historyItem = {
        id: notificationId,
        message: message,
        type: type,
        timestamp: new Date(),
        count: 1
      };

      this.history.unshift(historyItem);

      // Limit history size
      if (this.history.length > this.maxHistory) {
        this.history = this.history.slice(0, this.maxHistory);
      }

      // Save history
      this.saveHistory();

      // Update unread count
      this.updateUnreadCount(this.unreadCount + 1);

      // Limit the number of toasts
      if (this.toasts.length > this.maxToasts) {
        const oldestToast = this.toasts.shift();
        if (oldestToast && oldestToast.parentNode) {
          oldestToast.classList.remove('show');
          setTimeout(() => {
            if (oldestToast.parentNode) {
              oldestToast.parentNode.removeChild(oldestToast);
            }
          }, 300);
        }
      }

      // Show the toast with a slight delay for animation
      setTimeout(() => {
        toast.classList.add('show');
      }, 10);

      // Add close button event listener
      const closeBtn = toast.querySelector('.toast-close');
      if (closeBtn) {
        closeBtn.addEventListener('click', () => {
          this.dismiss(toast);
        });
      }

      // Auto-dismiss after duration
      if (duration > 0) {
        toast.dismissTimer = setTimeout(() => {
          this.dismiss(toast);
        }, duration);
      }

      return toast;
    },

    // Dismiss a toast
    dismiss(toast) {
      if (!toast) return;

      // Clear any dismiss timer
      if (toast.dismissTimer) {
        clearTimeout(toast.dismissTimer);
        toast.dismissTimer = null;
      }

      // Remove from DOM with animation
      toast.classList.remove('show');

      // Remove from tracking array
      const index = this.toasts.indexOf(toast);
      if (index > -1) {
        this.toasts.splice(index, 1);
      }

      // Remove from DOM after animation completes
      setTimeout(() => {
        if (toast.parentNode) {
          toast.parentNode.removeChild(toast);
        }
      }, 300);
    }
  };

  // Initialize the notification system immediately
  notificationManager.initializeOnLoad();

  // Shorthand function for showing a toast
  function showToast(message, type = 'info', duration = 3000) {
    return notificationManager.show(message, type, duration);
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

    // Initialize the notification system
    buildDashboardDescription();

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
            <div id="notification-bell-container"></div>
            <div id="connection-status" class="connection-status">● Connecting...</div>
          </div>
        </header>

        <div class="dashboard-info">
          <div class="dashboard-description">
            <strong>${dashboardName}</strong> - Real-time view of your pod statuses and health across applications.
            <br>
            <small>Data updates in real-time with an active connection. Filter by namespace using the dropdown above.</small>
          </div>
          <div class="last-updated" id="last-updated">
            <span class="refresh-icon">⟳</span> ${formattedTime}</div>
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

    // Add the notification bell to the header
    notificationManager.addBellToHeader();
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

    // Create container status indicators if available
    let statusContent = '';

    if (pod.containerStatuses && pod.containerStatuses.length > 0 && pod.status === 'Running') {
      // Create a container status badge with segments
      const containerStatusSegments = pod.containerStatuses.map(container => {
        const statusColor = getContainerStatusColor(container.status, container.ready);
        const title = `${container.name}: ${container.status}${container.reason ? ` (${container.reason})` : ''}`;
        return `<div class="container-status-segment" style="background-color: ${statusColor}" title="${title}"></div>`;
      }).join('');

      // Create the container status badge with ready count
      statusContent = `
        <div class="status-indicator" style="background-color: ${getStatusColor(pod.status)}">
          <span>${pod.status}</span>
          <div class="container-status-wrapper">
            <div class="container-status-segments">${containerStatusSegments}</div>
          </div>
        </div>
      `;
    } else {
      // Use the standard status indicator for non-Running pods or those without container info
      statusContent = `
        <span class="status-indicator" style="background-color: ${getStatusColor(pod.status)}">
          ${pod.status}
        </span>
      `;
    }

    return `
      <tr class="${missingClass}">
        <td class="pod-name">${pod.name}</td>
        <td>
          ${statusContent}
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
      lastUpdatedElement.innerHTML = `<span class="refresh-icon">⟳</span> ${formattedTime}`;
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

  // Get color for container status
  function getContainerStatusColor(status, ready) {
    // If container is ready, use green regardless of status
    if (ready) {
      return "#38a169"; // Green
    }

    // Otherwise, color based on status
    switch(status) {
      case "running": return "#f6ad55"; // Orange - running but not ready
      case "waiting": return "#e53e3e"; // Red
      case "terminated": return "#718096"; // Grey
      default: return "#cbd5e0"; // Light grey for unknown
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