/**
 * View: Service Dashboard
 */
ViewRouter.register('dashboard', {
  render: function(container) {
    container.innerHTML = '\
        <div class="stat-grid mb-xl">\
          <div class="stat-card"><div class="stat-card-icon blue"><i class="fas fa-tachometer-alt"></i></div><div class="stat-card-label">Request Rate</div><div class="stat-card-value">12.4K<span style="font-size:0.9rem;color:var(--text-secondary)"> /min</span></div><div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> 8.2% vs last hour</div></div>\
          <div class="stat-card"><div class="stat-card-icon green"><i class="fas fa-check-circle"></i></div><div class="stat-card-label">Success Rate</div><div class="stat-card-value">99.7<span style="font-size:0.9rem;color:var(--text-secondary)">%</span></div><div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> 0.1% vs last hour</div></div>\
          <div class="stat-card"><div class="stat-card-icon yellow"><i class="fas fa-stopwatch"></i></div><div class="stat-card-label">P99 Latency</div><div class="stat-card-value">247<span style="font-size:0.9rem;color:var(--text-secondary)"> ms</span></div><div class="stat-card-trend down"><i class="fas fa-arrow-down"></i> 12ms vs last hour</div></div>\
          <div class="stat-card"><div class="stat-card-icon red"><i class="fas fa-exclamation-triangle"></i></div><div class="stat-card-label">Error Rate</div><div class="stat-card-value">0.3<span style="font-size:0.9rem;color:var(--text-secondary)">%</span></div><div class="stat-card-trend neutral"><i class="fas fa-minus"></i> stable</div></div>\
          <div class="stat-card"><div class="stat-card-icon purple"><i class="fas fa-cubes"></i></div><div class="stat-card-label">Active Services</div><div class="stat-card-value">18</div><div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> +2 new</div></div>\
          <div class="stat-card" style="display:flex;align-items:center;gap:var(--space-lg)">\
            <div class="health-ring"><svg width="64" height="64" viewBox="0 0 64 64"><circle cx="32" cy="32" r="28" fill="none" stroke="var(--bg-tertiary)" stroke-width="6"/><circle cx="32" cy="32" r="28" fill="none" stroke="var(--accent-green)" stroke-width="6" stroke-dasharray="158" stroke-dashoffset="16" stroke-linecap="round"/></svg><span class="health-ring-value" style="color:var(--accent-green)">91</span></div>\
            <div><div class="stat-card-label">Apdex Score</div><div class="stat-card-value" style="font-size:1.4rem">0.91</div><div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> +0.02 vs last hour</div></div>\
          </div>\
        </div>\
        <div class="grid-2 mb-xl">\
          <div class="card"><div class="card-header"><span class="card-title"><i class="fas fa-chart-line" style="color:var(--accent-blue)"></i> Request Throughput</span><span class="text-xs text-tertiary">req/min</span></div><div class="card-body"><div class="chart-area" id="throughput-chart"></div></div></div>\
          <div class="card"><div class="card-header"><span class="card-title"><i class="fas fa-clock" style="color:var(--accent-yellow)"></i> Latency Distribution</span><span class="text-xs text-tertiary">P50 / P95 / P99</span></div><div class="card-body"><div class="chart-area" id="latency-chart"></div></div></div>\
        </div>\
        <div class="card">\
          <div class="card-header"><span class="card-title"><i class="fas fa-layer-group" style="color:var(--accent-blue)"></i> Service Health</span><button class="btn btn-ghost text-xs">View All \u2192</button></div>\
          <div class="table-container"><table>\
            <thead><tr><th>Service</th><th>Apdex</th><th>Health</th><th>Throughput</th><th>P99 Latency</th><th>Error Rate</th><th>Version</th><th>Instances</th></tr></thead>\
            <tbody>\
              <tr><td><span class="font-mono" style="color:var(--accent-blue)">order-service</span></td><td><span class="apdex-score apdex-good">0.97</span></td><td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Healthy</span></td><td class="font-mono">3,421/min</td><td class="font-mono">89ms</td><td class="font-mono" style="color:var(--accent-green)">0.1%</td><td><span class="badge badge-info">v2.3.1</span></td><td>4</td></tr>\
              <tr><td><span class="font-mono" style="color:var(--accent-blue)">payment-gateway</span></td><td><span class="apdex-score apdex-fair">0.72</span></td><td><span class="badge badge-warning"><span class="dot dot-warning"></span>Degraded</span></td><td class="font-mono">1,205/min</td><td class="font-mono" style="color:var(--accent-yellow)">412ms</td><td class="font-mono" style="color:var(--accent-yellow)">2.3%</td><td><span class="badge badge-warning"><i class="fas fa-arrow-up" style="font-size:0.5rem"></i> v1.8.0</span></td><td>3</td></tr>\
              <tr><td><span class="font-mono" style="color:var(--accent-blue)">user-auth</span></td><td><span class="apdex-score apdex-excellent">0.99</span></td><td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Healthy</span></td><td class="font-mono">5,678/min</td><td class="font-mono">34ms</td><td class="font-mono" style="color:var(--accent-green)">0.0%</td><td><span class="badge badge-info">v3.1.0</span></td><td>2</td></tr>\
              <tr><td><span class="font-mono" style="color:var(--accent-blue)">inventory-svc</span></td><td><span class="apdex-score apdex-poor">0.34</span></td><td><span class="badge badge-critical"><span class="dot dot-critical"></span>Critical</span></td><td class="font-mono">892/min</td><td class="font-mono" style="color:var(--accent-red)">1,240ms</td><td class="font-mono" style="color:var(--accent-red)">8.7%</td><td><span class="badge badge-critical"><i class="fas fa-arrow-up" style="font-size:0.5rem"></i> v1.2.4</span></td><td>2</td></tr>\
              <tr><td><span class="font-mono" style="color:var(--accent-blue)">notification-svc</span></td><td><span class="apdex-score apdex-good">0.95</span></td><td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Healthy</span></td><td class="font-mono">2,103/min</td><td class="font-mono">56ms</td><td class="font-mono" style="color:var(--accent-green)">0.2%</td><td><span class="badge badge-info">v1.5.2</span></td><td>3</td></tr>\
              <tr><td><span class="font-mono" style="color:var(--accent-blue)">recommendation-engine</span></td><td><span class="apdex-score apdex-good">0.93</span></td><td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Healthy</span></td><td class="font-mono">4,512/min</td><td class="font-mono">123ms</td><td class="font-mono" style="color:var(--accent-green)">0.4%</td><td><span class="badge badge-info">v4.0.1</span></td><td>5</td></tr>\
            </tbody>\
          </table></div>\
        </div>';
  },
  init: function() {
    Charts.initDashboardCharts();
  }
});
