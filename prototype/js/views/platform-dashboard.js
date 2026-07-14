/**
 * View: Platform Dashboard (Admin Only)
 */
ViewRouter.register('platform-dashboard', {
  render: function(container) {
    container.innerHTML = '\
        <!-- Platform Overview KPI -->\
        <div class="stat-grid mb-xl">\
          <div class="stat-card">\
            <div class="stat-card-icon blue"><i class="fas fa-building"></i></div>\
            <div class="stat-card-label">Active Tenants</div>\
            <div class="stat-card-value">3</div>\
            <div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> 1 new this month</div>\
          </div>\
          <div class="stat-card">\
            <div class="stat-card-icon green"><i class="fas fa-cogs"></i></div>\
            <div class="stat-card-label">Total Services</div>\
            <div class="stat-card-value">18</div>\
            <div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> 3 vs last month</div>\
          </div>\
          <div class="stat-card">\
            <div class="stat-card-icon purple"><i class="fas fa-server"></i></div>\
            <div class="stat-card-label">Total Instances</div>\
            <div class="stat-card-value">47</div>\
            <div class="stat-card-trend neutral"><i class="fas fa-minus"></i> Stable</div>\
          </div>\
          <div class="stat-card">\
            <div class="stat-card-icon yellow"><i class="fas fa-database"></i></div>\
            <div class="stat-card-label">Data Ingested</div>\
            <div class="stat-card-value">2.8<span style="font-size:0.9rem;color:var(--text-secondary)"> TB/day</span></div>\
            <div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> 12% vs last week</div>\
          </div>\
          <div class="stat-card">\
            <div class="stat-card-icon red"><i class="fas fa-exclamation-circle"></i></div>\
            <div class="stat-card-label">Active Alerts</div>\
            <div class="stat-card-value">5</div>\
            <div class="stat-card-trend down"><i class="fas fa-arrow-up"></i> 2 new today</div>\
          </div>\
          <div class="stat-card">\
            <div class="stat-card-icon green"><i class="fas fa-heartbeat"></i></div>\
            <div class="stat-card-label">Platform Health</div>\
            <div class="stat-card-value" style="color:var(--accent-green)">99.9<span style="font-size:0.9rem">%</span></div>\
            <div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> Healthy</div>\
          </div>\
        </div>\
\
        <!-- Tenant Health Overview -->\
        <div class="card mb-xl">\
          <div class="card-header">\
            <span class="card-title"><i class="fas fa-building"></i> Tenant Health Overview</span>\
            <span class="text-xs text-tertiary">All tenants</span>\
          </div>\
          <div class="card-body">\
            <div class="table-container">\
              <table>\
                <thead>\
                  <tr><th>Tenant</th><th>Apps</th><th>Services</th><th>Instances</th><th>Health</th><th>Data Ingested</th><th>Active Alerts</th><th>Status</th></tr>\
                </thead>\
                <tbody>\
                  <tr>\
                    <td><i class="fas fa-building" style="color:var(--accent-blue);margin-right:6px"></i><strong>Acme Corp</strong></td>\
                    <td class="font-mono">3</td><td class="font-mono">8</td><td class="font-mono">24</td>\
                    <td><span class="apdex-score apdex-good">0.91</span></td>\
                    <td class="font-mono">1.4 TB/day</td>\
                    <td><span class="error-count-badge">3</span></td>\
                    <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span> Active</span></td>\
                  </tr>\
                  <tr>\
                    <td><i class="fas fa-building" style="color:var(--accent-green);margin-right:6px"></i><strong>Beta Inc</strong></td>\
                    <td class="font-mono">1</td><td class="font-mono">4</td><td class="font-mono">10</td>\
                    <td><span class="apdex-score apdex-excellent">0.96</span></td>\
                    <td class="font-mono">680 GB/day</td>\
                    <td><span class="error-count-badge" style="background:rgba(210,153,34,0.15);color:var(--accent-yellow)">1</span></td>\
                    <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span> Active</span></td>\
                  </tr>\
                  <tr>\
                    <td><i class="fas fa-building" style="color:var(--accent-purple);margin-right:6px"></i><strong>Gamma Ltd</strong></td>\
                    <td class="font-mono">2</td><td class="font-mono">6</td><td class="font-mono">13</td>\
                    <td><span class="apdex-score apdex-fair">0.78</span></td>\
                    <td class="font-mono">720 GB/day</td>\
                    <td><span class="error-count-badge">8</span></td>\
                    <td><span class="badge badge-warning"><span class="dot dot-warning"></span> Degraded</span></td>\
                  </tr>\
                </tbody>\
              </table>\
            </div>\
          </div>\
        </div>\
\
        <!-- Quick Actions -->\
        <div class="grid-2">\
          <div class="card">\
            <div class="card-header">\
              <span class="card-title"><i class="fas fa-exclamation-triangle" style="color:var(--accent-red)"></i> Recent Cross-Tenant Errors</span>\
              <button class="btn btn-ghost text-xs" onclick="ViewRouter.navigate(\'global-errors\')">View All \u2192</button>\
            </div>\
            <div class="card-body" style="padding:0">\
              <table>\
                <tbody>\
                  <tr><td><span class="dot dot-critical"></span></td><td><code class="mono" style="color:var(--accent-red)">NullPointerException</code></td><td class="text-xs text-secondary">Acme Corp / order-service</td><td class="text-xs font-mono" style="color:var(--accent-red)">1,247</td></tr>\
                  <tr><td><span class="dot dot-critical"></span></td><td><code class="mono" style="color:var(--accent-red)">ConnectionTimeoutException</code></td><td class="text-xs text-secondary">Gamma Ltd / payment-svc</td><td class="text-xs font-mono" style="color:var(--accent-red)">893</td></tr>\
                  <tr><td><span class="dot dot-warning"></span></td><td><code class="mono" style="color:var(--accent-yellow)">RateLimitExceeded</code></td><td class="text-xs text-secondary">Beta Inc / api-gateway</td><td class="text-xs font-mono" style="color:var(--accent-yellow)">156</td></tr>\
                </tbody>\
              </table>\
            </div>\
          </div>\
          <div class="card">\
            <div class="card-header">\
              <span class="card-title"><i class="fas fa-chart-bar" style="color:var(--accent-blue)"></i> Resource Utilization</span>\
              <button class="btn btn-ghost text-xs" onclick="ViewRouter.navigate(\'resource-usage\')">Details \u2192</button>\
            </div>\
            <div class="card-body">\
              <div style="display:flex;flex-direction:column;gap:var(--space-lg)">\
                <div><div class="flex justify-between mb-sm"><span class="text-xs text-secondary">Span Ingestion</span><span class="text-xs font-mono">68%</span></div><div style="height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:68%;height:100%;background:var(--accent-blue);border-radius:3px"></div></div></div>\
                <div><div class="flex justify-between mb-sm"><span class="text-xs text-secondary">Metric Storage</span><span class="text-xs font-mono">45%</span></div><div style="height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:45%;height:100%;background:var(--accent-green);border-radius:3px"></div></div></div>\
                <div><div class="flex justify-between mb-sm"><span class="text-xs text-secondary">Log Storage</span><span class="text-xs font-mono">82%</span></div><div style="height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:82%;height:100%;background:var(--accent-yellow);border-radius:3px"></div></div></div>\
                <div><div class="flex justify-between mb-sm"><span class="text-xs text-secondary">Alert Quota</span><span class="text-xs font-mono">33%</span></div><div style="height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:33%;height:100%;background:var(--accent-green);border-radius:3px"></div></div></div>\
              </div>\
            </div>\
          </div>\
        </div>';
  }
});
