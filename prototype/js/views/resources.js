/**
 * Resource Explorer Page View
 * Tree navigation (App > Service > Instance) + Detail Panel
 */
ViewRouter.register('resources', {
  render: function(container) {
    container.innerHTML = `
      <div class="resource-explorer">
        <!-- Left: Tree Navigation -->
        <div class="re-tree-panel">
          <div class="re-tree-header">
            <div class="re-search">
              <i class="fas fa-search" style="color:var(--text-tertiary);font-size:0.75rem"></i>
              <input class="re-search-input" placeholder="Search apps, services, instances..." id="re-search-input">
            </div>
            <div class="re-tree-stats">
              <span class="text-xs text-tertiary">3 Apps \u00b7 8 Services \u00b7 16 Instances</span>
            </div>
          </div>
          <div class="re-tree-body" id="re-tree-body">
            <!-- App: E-Commerce Platform -->
            <div class="re-tree-node re-app" data-type="app" data-id="app-ecommerce">
              <div class="re-node-row re-node-row--app" onclick="toggleTreeNode(this)" data-select="app-ecommerce">
                <i class="fas fa-chevron-down re-chevron"></i>
                <i class="fas fa-cube re-icon" style="color:var(--accent-blue)"></i>
                <span class="re-node-name">E-Commerce Platform</span>
                <span class="re-node-count">5 svc \u00b7 12 inst</span>
                <span class="dot dot-healthy" style="margin-left:auto"></span>
              </div>
              <div class="re-children">
                <!-- Service: order-service -->
                <div class="re-tree-node re-service" data-type="service" data-id="svc-order">
                  <div class="re-node-row re-node-row--service" onclick="toggleTreeNode(this)" data-select="svc-order">
                    <i class="fas fa-chevron-down re-chevron"></i>
                    <i class="fas fa-cogs re-icon" style="color:var(--accent-green)"></i>
                    <span class="re-node-name">order-service</span>
                    <span class="re-node-count">4 inst</span>
                    <span class="dot dot-healthy" style="margin-left:auto"></span>
                  </div>
                  <div class="re-children">
                    <div class="re-tree-node re-instance" data-type="instance" data-id="inst-order-1">
                      <div class="re-node-row re-node-row--instance re-node-row--selected" data-select="inst-order-1">
                        <span class="re-leaf-spacer"></span>
                        <span class="dot dot-healthy"></span>
                        <span class="re-node-name font-mono">pod-7f4d8</span>
                        <span class="text-xs text-tertiary" style="margin-left:auto">10.0.2.15</span>
                      </div>
                    </div>
                    <div class="re-tree-node re-instance" data-type="instance" data-id="inst-order-2">
                      <div class="re-node-row re-node-row--instance" data-select="inst-order-2">
                        <span class="re-leaf-spacer"></span>
                        <span class="dot dot-healthy"></span>
                        <span class="re-node-name font-mono">pod-a2b3c</span>
                        <span class="text-xs text-tertiary" style="margin-left:auto">10.0.2.16</span>
                      </div>
                    </div>
                    <div class="re-tree-node re-instance" data-type="instance" data-id="inst-order-3">
                      <div class="re-node-row re-node-row--instance" data-select="inst-order-3">
                        <span class="re-leaf-spacer"></span>
                        <span class="dot dot-healthy"></span>
                        <span class="re-node-name font-mono">pod-d4e5f</span>
                        <span class="text-xs text-tertiary" style="margin-left:auto">10.0.2.17</span>
                      </div>
                    </div>
                    <div class="re-tree-node re-instance" data-type="instance" data-id="inst-order-4">
                      <div class="re-node-row re-node-row--instance" data-select="inst-order-4">
                        <span class="re-leaf-spacer"></span>
                        <span class="dot dot-healthy"></span>
                        <span class="re-node-name font-mono">pod-g7h8i</span>
                        <span class="text-xs text-tertiary" style="margin-left:auto">10.0.2.18</span>
                      </div>
                    </div>
                  </div>
                </div>
                <!-- Service: payment-gateway -->
                <div class="re-tree-node re-service" data-type="service" data-id="svc-payment">
                  <div class="re-node-row re-node-row--service" onclick="toggleTreeNode(this)" data-select="svc-payment">
                    <i class="fas fa-chevron-down re-chevron"></i>
                    <i class="fas fa-cogs re-icon" style="color:var(--accent-yellow)"></i>
                    <span class="re-node-name">payment-gateway</span>
                    <span class="re-node-count">3 inst</span>
                    <span class="dot dot-warning" style="margin-left:auto"></span>
                  </div>
                  <div class="re-children">
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-pay-1"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-x9y8z</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.3.21</span></div></div>
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-pay-2"><span class="re-leaf-spacer"></span><span class="dot dot-warning"></span><span class="re-node-name font-mono">pod-w7v6u</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.3.22</span></div></div>
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-pay-3"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-t5s4r</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.3.23</span></div></div>
                  </div>
                </div>
                <!-- Service: inventory-svc -->
                <div class="re-tree-node re-service" data-type="service" data-id="svc-inventory">
                  <div class="re-node-row re-node-row--service" onclick="toggleTreeNode(this)" data-select="svc-inventory">
                    <i class="fas fa-chevron-down re-chevron"></i>
                    <i class="fas fa-cogs re-icon" style="color:var(--accent-red)"></i>
                    <span class="re-node-name">inventory-svc</span>
                    <span class="re-node-count">2 inst</span>
                    <span class="dot dot-critical" style="margin-left:auto"></span>
                  </div>
                  <div class="re-children">
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-inv-1"><span class="re-leaf-spacer"></span><span class="dot dot-critical"></span><span class="re-node-name font-mono">pod-m1n2o</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.4.8</span></div></div>
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-inv-2"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-p3q4r</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.4.9</span></div></div>
                  </div>
                </div>
                <!-- Service: user-auth (collapsed) -->
                <div class="re-tree-node re-service" data-type="service" data-id="svc-auth">
                  <div class="re-node-row re-node-row--service" onclick="toggleTreeNode(this)" data-select="svc-auth">
                    <i class="fas fa-chevron-right re-chevron"></i>
                    <i class="fas fa-cogs re-icon" style="color:var(--accent-green)"></i>
                    <span class="re-node-name">user-auth</span>
                    <span class="re-node-count">2 inst</span>
                    <span class="dot dot-healthy" style="margin-left:auto"></span>
                  </div>
                  <div class="re-children" style="display:none">
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-auth-1"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-k5l6m</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.1.5</span></div></div>
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-auth-2"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-n7o8p</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.1.6</span></div></div>
                  </div>
                </div>
                <!-- Service: notification-svc (collapsed) -->
                <div class="re-tree-node re-service" data-type="service" data-id="svc-notif">
                  <div class="re-node-row re-node-row--service" onclick="toggleTreeNode(this)" data-select="svc-notif">
                    <i class="fas fa-chevron-right re-chevron"></i>
                    <i class="fas fa-cogs re-icon" style="color:var(--accent-green)"></i>
                    <span class="re-node-name">notification-svc</span>
                    <span class="re-node-count">1 inst</span>
                    <span class="dot dot-healthy" style="margin-left:auto"></span>
                  </div>
                  <div class="re-children" style="display:none">
                    <div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-notif-1"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-q1r2s</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.5.3</span></div></div>
                  </div>
                </div>
              </div>
            </div>

            <!-- App: Internal Tools -->
            <div class="re-tree-node re-app" data-type="app" data-id="app-internal">
              <div class="re-node-row re-node-row--app" onclick="toggleTreeNode(this)" data-select="app-internal">
                <i class="fas fa-chevron-right re-chevron"></i>
                <i class="fas fa-cube re-icon" style="color:var(--accent-purple)"></i>
                <span class="re-node-name">Internal Tools</span>
                <span class="re-node-count">2 svc \u00b7 3 inst</span>
                <span class="dot dot-healthy" style="margin-left:auto"></span>
              </div>
              <div class="re-children" style="display:none">
                <div class="re-tree-node re-service"><div class="re-node-row re-node-row--service" onclick="toggleTreeNode(this)" data-select="svc-admin"><i class="fas fa-chevron-right re-chevron"></i><i class="fas fa-cogs re-icon" style="color:var(--accent-green)"></i><span class="re-node-name">admin-portal</span><span class="re-node-count">2 inst</span><span class="dot dot-healthy" style="margin-left:auto"></span></div><div class="re-children" style="display:none"><div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-admin-1"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-adm01</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.6.1</span></div></div><div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-admin-2"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-adm02</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.6.2</span></div></div></div></div>
                <div class="re-tree-node re-service"><div class="re-node-row re-node-row--service" data-select="svc-scheduler"><i class="fas fa-chevron-right re-chevron"></i><i class="fas fa-cogs re-icon" style="color:var(--accent-green)"></i><span class="re-node-name">task-scheduler</span><span class="re-node-count">1 inst</span><span class="dot dot-healthy" style="margin-left:auto"></span></div><div class="re-children" style="display:none"><div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-sched-1"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-sch01</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.7.1</span></div></div></div></div>
              </div>
            </div>

            <!-- App: Data Pipeline -->
            <div class="re-tree-node re-app" data-type="app" data-id="app-pipeline">
              <div class="re-node-row re-node-row--app" onclick="toggleTreeNode(this)" data-select="app-pipeline">
                <i class="fas fa-chevron-right re-chevron"></i>
                <i class="fas fa-cube re-icon" style="color:var(--accent-cyan)"></i>
                <span class="re-node-name">Data Pipeline</span>
                <span class="re-node-count">1 svc \u00b7 1 inst</span>
                <span class="dot dot-healthy" style="margin-left:auto"></span>
              </div>
              <div class="re-children" style="display:none">
                <div class="re-tree-node re-service"><div class="re-node-row re-node-row--service" data-select="svc-etl"><i class="fas fa-chevron-right re-chevron"></i><i class="fas fa-cogs re-icon" style="color:var(--accent-green)"></i><span class="re-node-name">etl-worker</span><span class="re-node-count">1 inst</span><span class="dot dot-healthy" style="margin-left:auto"></span></div><div class="re-children" style="display:none"><div class="re-tree-node re-instance"><div class="re-node-row re-node-row--instance" data-select="inst-etl-1"><span class="re-leaf-spacer"></span><span class="dot dot-healthy"></span><span class="re-node-name font-mono">pod-etl01</span><span class="text-xs text-tertiary" style="margin-left:auto">10.0.8.1</span></div></div></div></div>
              </div>
            </div>
          </div>
        </div>

        <!-- Right: Detail Panel -->
        <div class="re-detail-panel" id="re-detail-panel">
          <div class="re-detail-header">
            <div class="re-detail-breadcrumb">
              <span class="text-xs" style="color:var(--accent-blue);cursor:pointer">E-Commerce Platform</span>
              <i class="fas fa-chevron-right" style="font-size:0.5rem;color:var(--text-tertiary)"></i>
              <span class="text-xs" style="color:var(--accent-blue);cursor:pointer">order-service</span>
              <i class="fas fa-chevron-right" style="font-size:0.5rem;color:var(--text-tertiary)"></i>
              <span class="text-xs text-primary">pod-7f4d8</span>
            </div>
            <div class="re-detail-title">
              <span class="dot dot-healthy" style="width:10px;height:10px"></span>
              <h2 style="font-size:1.1rem;font-weight:600">pod-7f4d8</h2>
              <span class="badge badge-healthy">Online</span>
              <span class="badge badge-info" style="margin-left:auto">Agent v1.4.2</span>
            </div>
          </div>

          <!-- Quick Stats -->
          <div class="re-detail-stats">
            <div class="re-mini-stat">
              <div class="re-mini-stat-label">CPU</div>
              <div class="re-mini-stat-value">35%</div>
              <div class="re-mini-stat-bar"><div class="re-mini-stat-fill" style="width:35%;background:var(--accent-green)"></div></div>
            </div>
            <div class="re-mini-stat">
              <div class="re-mini-stat-label">Memory</div>
              <div class="re-mini-stat-value">62%</div>
              <div class="re-mini-stat-bar"><div class="re-mini-stat-fill" style="width:62%;background:var(--accent-yellow)"></div></div>
            </div>
            <div class="re-mini-stat">
              <div class="re-mini-stat-label">GC Pause</div>
              <div class="re-mini-stat-value" style="color:var(--accent-green)">12ms</div>
              <div class="re-mini-stat-bar"><div class="re-mini-stat-fill" style="width:12%;background:var(--accent-green)"></div></div>
            </div>
            <div class="re-mini-stat">
              <div class="re-mini-stat-label">Uptime</div>
              <div class="re-mini-stat-value">3d 14h</div>
              <div class="re-mini-stat-bar"><div class="re-mini-stat-fill" style="width:90%;background:var(--accent-blue)"></div></div>
            </div>
          </div>

          <!-- Tabs -->
          <div class="tabs" style="padding:0 var(--space-lg)">
            <div class="tab active">Overview</div>
            <div class="tab">Metrics</div>
            <div class="tab">Tasks</div>
            <div class="tab">Arthas</div>
            <div class="tab">Instrumentation</div>
          </div>

          <!-- Tab Content: Overview -->
          <div class="re-detail-content">
            <!-- Instance Info -->
            <div class="card mb-lg">
              <div class="card-header"><span class="card-title"><i class="fas fa-info-circle" style="color:var(--accent-blue)"></i> Instance Info</span></div>
              <div class="card-body">
                <div style="display:grid;grid-template-columns:1fr 1fr;gap:var(--space-md)">
                  <div><div class="text-xs text-tertiary">Agent ID</div><div class="font-mono text-sm" style="margin-top:2px">agent-7f4d8b2c</div></div>
                  <div><div class="text-xs text-tertiary">Hostname</div><div class="font-mono text-sm" style="margin-top:2px">order-svc-pod-7f4d8</div></div>
                  <div><div class="text-xs text-tertiary">IP Address</div><div class="font-mono text-sm" style="margin-top:2px">10.0.2.15</div></div>
                  <div><div class="text-xs text-tertiary">Java Version</div><div class="font-mono text-sm" style="margin-top:2px">OpenJDK 17.0.9</div></div>
                  <div><div class="text-xs text-tertiary">OS</div><div class="font-mono text-sm" style="margin-top:2px">Linux 5.15.0 (amd64)</div></div>
                  <div><div class="text-xs text-tertiary">Last Heartbeat</div><div class="font-mono text-sm" style="margin-top:2px;color:var(--accent-green)">2s ago</div></div>
                </div>
              </div>
            </div>

            <!-- Active Instrumentation Rules -->
            <div class="card mb-lg">
              <div class="card-header"><span class="card-title"><i class="fas fa-microscope" style="color:var(--accent-purple)"></i> Active Rules (2)</span></div>
              <div class="card-body" style="padding:var(--space-sm) var(--space-lg)">
                <div style="display:flex;flex-direction:column;gap:var(--space-sm)">
                  <div class="flex items-center justify-between" style="padding:var(--space-sm) var(--space-md);background:var(--bg-tertiary);border-radius:var(--radius-md)">
                    <div class="flex items-center gap-sm"><span class="dot dot-healthy"></span><span class="font-mono text-xs">OrderService.submit</span></div>
                    <span class="badge badge-healthy">applied</span>
                  </div>
                  <div class="flex items-center justify-between" style="padding:var(--space-sm) var(--space-md);background:var(--bg-tertiary);border-radius:var(--radius-md)">
                    <div class="flex items-center gap-sm"><span class="dot dot-healthy"></span><span class="font-mono text-xs">OrderService.getById</span></div>
                    <span class="badge badge-healthy">applied</span>
                  </div>
                </div>
              </div>
            </div>

            <!-- Recent Tasks -->
            <div class="card">
              <div class="card-header"><span class="card-title"><i class="fas fa-tasks" style="color:var(--accent-yellow)"></i> Recent Tasks</span></div>
              <div class="card-body" style="padding:var(--space-sm) var(--space-lg)">
                <div style="display:flex;flex-direction:column;gap:var(--space-sm)">
                  <div class="flex items-center justify-between" style="padding:var(--space-sm) var(--space-md);background:var(--bg-tertiary);border-radius:var(--radius-md)">
                    <div><span class="font-mono text-xs">dynamic_instrument</span><span class="text-xs text-tertiary" style="margin-left:8px">5 min ago</span></div>
                    <span class="badge badge-healthy">success</span>
                  </div>
                  <div class="flex items-center justify-between" style="padding:var(--space-sm) var(--space-md);background:var(--bg-tertiary);border-radius:var(--radius-md)">
                    <div><span class="font-mono text-xs">config_push</span><span class="text-xs text-tertiary" style="margin-left:8px">1 hour ago</span></div>
                    <span class="badge badge-healthy">success</span>
                  </div>
                </div>
              </div>
            </div>

            <!-- Quick Actions -->
            <div style="margin-top:var(--space-lg);display:flex;gap:var(--space-md)">
              <button class="btn btn-ghost"><i class="fas fa-terminal"></i> Open Arthas</button>
              <button class="btn btn-ghost"><i class="fas fa-plus"></i> New Task</button>
              <button class="btn btn-ghost"><i class="fas fa-chart-line"></i> View Metrics</button>
              <button class="btn btn-ghost" style="color:var(--accent-red)"><i class="fas fa-power-off"></i> Unregister</button>
            </div>
          </div>
        </div>
      </div>
    `;
  },

  init: function(container) {
    // Tree node toggle
    window.toggleTreeNode = function(el) {
      var nodeRow = el.closest ? el.closest('.re-node-row') : el;
      var treeNode = nodeRow.parentElement;
      var children = treeNode.querySelector('.re-children');
      var chevron = nodeRow.querySelector('.re-chevron');
      if (!children) return;

      var isCollapsed = children.style.display === 'none';
      children.style.display = isCollapsed ? '' : 'none';
      if (chevron) {
        chevron.classList.toggle('fa-chevron-down', isCollapsed);
        chevron.classList.toggle('fa-chevron-right', !isCollapsed);
      }
    };

    // Node selection
    container.querySelectorAll('.re-node-row[data-select]').forEach(function(row) {
      row.addEventListener('click', function(e) {
        if (e.target.classList.contains('re-chevron')) return;
        container.querySelectorAll('.re-node-row--selected').forEach(function(r) { r.classList.remove('re-node-row--selected'); });
        row.classList.add('re-node-row--selected');
      });
    });

    // Search filter
    var searchInput = container.querySelector('#re-search-input');
    if (searchInput) {
      searchInput.addEventListener('input', function(e) {
        var query = e.target.value.toLowerCase().trim();
        container.querySelectorAll('.re-tree-node').forEach(function(node) {
          var name = node.querySelector('.re-node-name');
          if (!name) return;
          var match = !query || name.textContent.toLowerCase().includes(query);
          node.style.display = match ? '' : 'none';
          if (match && query) {
            var parent = node.parentElement;
            while (parent) {
              if (parent.classList && parent.classList.contains('re-tree-node')) parent.style.display = '';
              if (parent.classList && parent.classList.contains('re-children')) parent.style.display = '';
              parent = parent.parentElement;
            }
          }
        });
      });
    }
  }
});
