/**
 * View: Metrics
 */
ViewRouter.register('metrics', {
  render: function(container) {
    container.innerHTML = '\
        <div class="tabs"><div class="tab active">RED Dashboard</div><div class="tab">Endpoints</div><div class="tab">Custom Query</div><div class="tab">Saved Panels</div></div>\
        <div class="stat-grid mb-xl" style="grid-template-columns:repeat(3,1fr)">\
          <div class="stat-card"><div class="stat-card-label">Avg Response Time</div><div class="stat-card-value">124<span style="font-size:0.9rem;color:var(--text-secondary)"> ms</span></div></div>\
          <div class="stat-card"><div class="stat-card-label">Throughput</div><div class="stat-card-value">8.2K<span style="font-size:0.9rem;color:var(--text-secondary)"> rpm</span></div></div>\
          <div class="stat-card"><div class="stat-card-label">Error Count</div><div class="stat-card-value" style="color:var(--accent-red)">47</div></div>\
        </div>\
        <div class="grid-2 mb-lg">\
          <div class="card"><div class="card-header"><span class="card-title">Rate \u2014 Requests per minute</span></div><div class="card-body"><div class="chart-area" id="metrics-rate-chart"></div></div></div>\
          <div class="card"><div class="card-header"><span class="card-title">Errors \u2014 Error count by service</span></div><div class="card-body"><div class="chart-area" id="metrics-errors-chart"></div></div></div>\
        </div>\
        <div class="grid-2 mb-lg">\
          <div class="card"><div class="card-header"><span class="card-title">Duration \u2014 Response time percentiles</span></div><div class="card-body"><div class="chart-area" id="metrics-duration-chart"></div></div></div>\
          <div class="card"><div class="card-header"><span class="card-title">Saturation \u2014 CPU / Memory</span></div><div class="card-body"><div class="chart-area" id="metrics-saturation-chart"></div></div></div>\
        </div>\
        <div class="card">\
          <div class="card-header"><span class="card-title"><i class="fas fa-exchange-alt" style="color:var(--accent-purple)"></i> Top Endpoints by P99 Latency</span><span class="text-xs text-tertiary">Sorted by slowest P99</span></div>\
          <div class="table-container"><table>\
            <thead><tr><th>Method</th><th>Endpoint</th><th>Service</th><th>Throughput</th><th>P50</th><th>P95</th><th>P99</th><th>Error Rate</th><th>Apdex</th></tr></thead>\
            <tbody>\
              <tr><td><span class="endpoint-method endpoint-method--get">GET</span></td><td><span class="endpoint-path">/api/inventory/stock/{sku}</span></td><td>inventory-svc</td><td class="font-mono">892/min</td><td class="font-mono">340ms</td><td class="font-mono" style="color:var(--accent-yellow)">980ms</td><td class="font-mono" style="color:var(--accent-red)">2,100ms</td><td class="font-mono" style="color:var(--accent-red)">8.7%</td><td><span class="apdex-score apdex-poor">0.28</span></td></tr>\
              <tr><td><span class="endpoint-method endpoint-method--post">POST</span></td><td><span class="endpoint-path">/api/payments/charge</span></td><td>payment-gateway</td><td class="font-mono">1,205/min</td><td class="font-mono">180ms</td><td class="font-mono" style="color:var(--accent-yellow)">450ms</td><td class="font-mono" style="color:var(--accent-yellow)">890ms</td><td class="font-mono" style="color:var(--accent-yellow)">2.3%</td><td><span class="apdex-score apdex-fair">0.72</span></td></tr>\
              <tr><td><span class="endpoint-method endpoint-method--post">POST</span></td><td><span class="endpoint-path">/api/orders</span></td><td>order-service</td><td class="font-mono">3,421/min</td><td class="font-mono">45ms</td><td class="font-mono">120ms</td><td class="font-mono">245ms</td><td class="font-mono" style="color:var(--accent-green)">0.1%</td><td><span class="apdex-score apdex-good">0.97</span></td></tr>\
              <tr><td><span class="endpoint-method endpoint-method--get">GET</span></td><td><span class="endpoint-path">/api/orders/{id}</span></td><td>order-service</td><td class="font-mono">5,120/min</td><td class="font-mono">22ms</td><td class="font-mono">68ms</td><td class="font-mono">142ms</td><td class="font-mono" style="color:var(--accent-green)">0.05%</td><td><span class="apdex-score apdex-excellent">0.99</span></td></tr>\
              <tr><td><span class="endpoint-method endpoint-method--post">POST</span></td><td><span class="endpoint-path">/auth/login</span></td><td>user-auth</td><td class="font-mono">5,678/min</td><td class="font-mono">18ms</td><td class="font-mono">45ms</td><td class="font-mono">89ms</td><td class="font-mono" style="color:var(--accent-green)">0.0%</td><td><span class="apdex-score apdex-excellent">0.99</span></td></tr>\
              <tr><td><span class="endpoint-method endpoint-method--put">PUT</span></td><td><span class="endpoint-path">/api/orders/{id}/status</span></td><td>order-service</td><td class="font-mono">1,890/min</td><td class="font-mono">35ms</td><td class="font-mono">78ms</td><td class="font-mono">120ms</td><td class="font-mono" style="color:var(--accent-green)">0.2%</td><td><span class="apdex-score apdex-good">0.96</span></td></tr>\
              <tr><td><span class="endpoint-method endpoint-method--delete">DELETE</span></td><td><span class="endpoint-path">/api/cart/items/{id}</span></td><td>order-service</td><td class="font-mono">780/min</td><td class="font-mono">12ms</td><td class="font-mono">28ms</td><td class="font-mono">56ms</td><td class="font-mono" style="color:var(--accent-green)">0.0%</td><td><span class="apdex-score apdex-excellent">1.00</span></td></tr>\
            </tbody>\
          </table></div>\
        </div>';
  },
  init: function() {
    Charts.initMetricsCharts();
  }
});
