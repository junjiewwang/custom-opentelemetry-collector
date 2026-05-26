/**
 * View: Resource Usage (Admin Only)
 */
ViewRouter.register('resource-usage', {
  render: function(container) {
    container.innerHTML = '\
        <div class="stat-grid mb-xl">\
          <div class="stat-card"><div class="stat-card-icon blue"><i class="fas fa-stream"></i></div><div class="stat-card-label">Spans / Day</div><div class="stat-card-value">1.2<span style="font-size:0.9rem;color:var(--text-secondary)">B</span></div><div class="stat-card-trend up"><i class="fas fa-arrow-up"></i> 15% vs last week</div></div>\
          <div class="stat-card"><div class="stat-card-icon green"><i class="fas fa-database"></i></div><div class="stat-card-label">Metric Series</div><div class="stat-card-value">840<span style="font-size:0.9rem;color:var(--text-secondary)">K</span></div><div class="stat-card-trend neutral"><i class="fas fa-minus"></i> Stable</div></div>\
          <div class="stat-card"><div class="stat-card-icon purple"><i class="fas fa-hdd"></i></div><div class="stat-card-label">Storage Used</div><div class="stat-card-value">14.2<span style="font-size:0.9rem;color:var(--text-secondary)"> TB</span></div><div class="stat-card-trend down"><i class="fas fa-arrow-up"></i> Growing 3%/week</div></div>\
          <div class="stat-card"><div class="stat-card-icon yellow"><i class="fas fa-percentage"></i></div><div class="stat-card-label">Quota Usage</div><div class="stat-card-value">62<span style="font-size:0.9rem;color:var(--text-secondary)">%</span></div><div class="stat-card-trend neutral"><i class="fas fa-minus"></i> Within limits</div></div>\
        </div>\
        <div class="card mb-xl">\
          <div class="card-header"><span class="card-title"><i class="fas fa-chart-bar"></i> Per-Tenant Resource Usage</span><span class="text-xs text-tertiary">Last 30 days</span></div>\
          <div class="card-body" style="padding:0"><div class="table-container"><table>\
            <thead><tr><th>Tenant</th><th>Spans/Day</th><th>Metrics Series</th><th>Storage</th><th>Quota</th><th>Quota Usage</th><th>Trend (7d)</th></tr></thead>\
            <tbody>\
              <tr><td><i class="fas fa-building" style="color:var(--accent-blue);margin-right:6px"></i><strong>Acme Corp</strong></td><td class="font-mono">580M</td><td class="font-mono">420K</td><td class="font-mono">7.1 TB</td><td class="font-mono">10 TB</td><td><div style="display:flex;align-items:center;gap:8px"><div style="flex:1;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:71%;height:100%;background:var(--accent-yellow);border-radius:3px"></div></div><span class="text-xs font-mono" style="color:var(--accent-yellow)">71%</span></div></td><td><span class="error-sparkline">\u2582\u2583\u2583\u2585\u2585\u2586\u2587</span></td></tr>\
              <tr><td><i class="fas fa-building" style="color:var(--accent-green);margin-right:6px"></i><strong>Beta Inc</strong></td><td class="font-mono">280M</td><td class="font-mono">180K</td><td class="font-mono">3.2 TB</td><td class="font-mono">8 TB</td><td><div style="display:flex;align-items:center;gap:8px"><div style="flex:1;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:40%;height:100%;background:var(--accent-green);border-radius:3px"></div></div><span class="text-xs font-mono">40%</span></div></td><td><span class="error-sparkline">\u2583\u2583\u2583\u2583\u2583\u2583\u2583</span></td></tr>\
              <tr><td><i class="fas fa-building" style="color:var(--accent-purple);margin-right:6px"></i><strong>Gamma Ltd</strong></td><td class="font-mono">340M</td><td class="font-mono">240K</td><td class="font-mono">3.9 TB</td><td class="font-mono">5 TB</td><td><div style="display:flex;align-items:center;gap:8px"><div style="flex:1;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:78%;height:100%;background:var(--accent-red);border-radius:3px"></div></div><span class="text-xs font-mono" style="color:var(--accent-red)">78%</span></div></td><td><span class="error-sparkline" style="color:var(--accent-red)">\u2583\u2585\u2585\u2586\u2587\u2587\u2588</span></td></tr>\
            </tbody>\
          </table></div></div>\
        </div>\
        <div class="card">\
          <div class="card-header"><span class="card-title"><i class="fas fa-chart-line"></i> Storage Growth Trend</span></div>\
          <div class="card-body"><div class="chart-area" id="storage-trend-chart"></div></div>\
        </div>';
  },
  init: function() {
    Charts.initResourceUsageCharts();
  }
});
