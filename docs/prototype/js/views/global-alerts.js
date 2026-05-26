/**
 * View: Global Alerts (Admin Only)
 */
ViewRouter.register('global-alerts', {
  render: function(container) {
    container.innerHTML = '\
        <div class="stat-grid mb-xl">\
          <div class="stat-card"><div class="stat-card-icon red"><i class="fas fa-bell"></i></div><div class="stat-card-label">Active Alerts</div><div class="stat-card-value" style="color:var(--accent-red)">5</div><div class="stat-card-trend down"><i class="fas fa-arrow-up"></i> 2 new</div></div>\
          <div class="stat-card"><div class="stat-card-icon yellow"><i class="fas fa-clock"></i></div><div class="stat-card-label">Avg Response Time</div><div class="stat-card-value">8<span style="font-size:0.9rem;color:var(--text-secondary)"> min</span></div><div class="stat-card-trend up"><i class="fas fa-arrow-down"></i> -25%</div></div>\
          <div class="stat-card"><div class="stat-card-icon green"><i class="fas fa-check-circle"></i></div><div class="stat-card-label">Resolved (24h)</div><div class="stat-card-value">7</div><div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> Good pace</div></div>\
        </div>\
        <div class="card">\
          <div class="card-header"><span class="card-title"><i class="fas fa-bell"></i> Cross-Tenant Alerts</span><div style="display:flex;gap:var(--space-sm)"><span class="badge badge-critical"><span class="dot dot-critical"></span> Critical 2</span><span class="badge badge-warning"><span class="dot dot-warning"></span> Warning 3</span></div></div>\
          <div class="card-body" style="padding:0"><div class="table-container"><table>\
            <thead><tr><th></th><th>Alert</th><th>Tenant</th><th>Service</th><th>Severity</th><th>Duration</th><th>Actions</th></tr></thead>\
            <tbody>\
              <tr><td><span class="dot dot-critical" style="animation:pulse 2s infinite"></span></td><td><div><strong>Error rate > 5% for 10min</strong></div><div class="text-xs text-tertiary">Error rate spiked to 8.3% after deployment v2.4.1</div></td><td class="text-sm">Acme Corp</td><td><span class="badge badge-info">order-service</span></td><td><span class="badge badge-critical">Critical</span></td><td class="font-mono text-xs" style="color:var(--accent-red)">23min</td><td><div style="display:flex;gap:4px"><button class="btn btn-ghost text-xs"><i class="fas fa-check"></i> Ack</button><button class="btn btn-ghost text-xs"><i class="fas fa-external-link-alt"></i></button></div></td></tr>\
              <tr><td><span class="dot dot-critical" style="animation:pulse 2s infinite"></span></td><td><div><strong>P99 latency > 2s for 5min</strong></div><div class="text-xs text-tertiary">Redis connection pool exhaustion causing timeout cascade</div></td><td class="text-sm">Gamma Ltd</td><td><span class="badge badge-info">payment-svc</span></td><td><span class="badge badge-critical">Critical</span></td><td class="font-mono text-xs" style="color:var(--accent-red)">45min</td><td><div style="display:flex;gap:4px"><button class="btn btn-ghost text-xs"><i class="fas fa-check"></i> Ack</button><button class="btn btn-ghost text-xs"><i class="fas fa-external-link-alt"></i></button></div></td></tr>\
              <tr><td><span class="dot dot-warning"></span></td><td><div><strong>CPU usage > 80% for 15min</strong></div><div class="text-xs text-tertiary">Instance pod-x9y0z approaching resource limit</div></td><td class="text-sm">Acme Corp</td><td><span class="badge badge-info">inventory-svc</span></td><td><span class="badge badge-warning">Warning</span></td><td class="font-mono text-xs">1h 12min</td><td><div style="display:flex;gap:4px"><button class="btn btn-ghost text-xs"><i class="fas fa-check"></i> Ack</button><button class="btn btn-ghost text-xs"><i class="fas fa-external-link-alt"></i></button></div></td></tr>\
              <tr><td><span class="dot dot-warning"></span></td><td><div><strong>Storage quota > 70%</strong></div><div class="text-xs text-tertiary">Tenant approaching storage limit, consider plan upgrade</div></td><td class="text-sm">Acme Corp</td><td><span class="badge badge-neutral">Platform</span></td><td><span class="badge badge-warning">Warning</span></td><td class="font-mono text-xs">3h 40min</td><td><div style="display:flex;gap:4px"><button class="btn btn-ghost text-xs"><i class="fas fa-check"></i> Ack</button><button class="btn btn-ghost text-xs"><i class="fas fa-external-link-alt"></i></button></div></td></tr>\
              <tr><td><span class="dot dot-warning"></span></td><td><div><strong>Agent version outdated</strong></div><div class="text-xs text-tertiary">Running v1.7.9, recommend upgrade to v1.8.2</div></td><td class="text-sm">Gamma Ltd</td><td><span class="badge badge-neutral">Platform</span></td><td><span class="badge badge-warning">Warning</span></td><td class="font-mono text-xs">5d</td><td><div style="display:flex;gap:4px"><button class="btn btn-ghost text-xs"><i class="fas fa-check"></i> Ack</button><button class="btn btn-ghost text-xs"><i class="fas fa-external-link-alt"></i></button></div></td></tr>\
            </tbody>\
          </table></div></div>\
        </div>';
  }
});
