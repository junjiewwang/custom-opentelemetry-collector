/**
 * Alerts Page View
 * Shows active alerts with tabs for Active/Rules/History
 */
ViewRouter.register('alerts', {
  render: function(container) {
    container.innerHTML = `
      <div class="tabs">
        <div class="tab active">Active Alerts <span class="nav-badge" style="margin-left:4px">3</span></div>
        <div class="tab">Alert Rules</div>
        <div class="tab">History</div>
      </div>

      <!-- Active Alerts -->
      <div style="display:flex;flex-direction:column;gap:var(--space-md)">
        <div class="card" style="border-left:3px solid var(--accent-red)">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-md">
                <span class="dot dot-critical" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem">High Error Rate \u2014 inventory-svc</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">Error rate exceeded 5% threshold for 5 minutes</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="text-xs text-tertiary">Firing since 12 min ago</span>
                <button class="btn btn-ghost text-xs">Acknowledge</button>
              </div>
            </div>
          </div>
        </div>
        <div class="card" style="border-left:3px solid var(--accent-red)">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-md">
                <span class="dot dot-critical" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem">P99 Latency Spike \u2014 inventory-svc</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">P99 latency > 1000ms for 3 consecutive minutes</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="text-xs text-tertiary">Firing since 10 min ago</span>
                <button class="btn btn-ghost text-xs">Acknowledge</button>
              </div>
            </div>
          </div>
        </div>
        <div class="card" style="border-left:3px solid var(--accent-yellow)">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-md">
                <span class="dot dot-warning" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem">High CPU Usage \u2014 payment-gw-pod-x9y8z</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">CPU utilization exceeded 80% for 10 minutes</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="text-xs text-tertiary">Firing since 6 min ago</span>
                <button class="btn btn-ghost text-xs">Acknowledge</button>
              </div>
            </div>
          </div>
        </div>
      </div>
    `;
  }
});
