/**
 * Error Inbox Page View
 * Shows error groups with status badges and sparklines
 */
ViewRouter.register('errors', {
  render: function(container) {
    container.innerHTML = `
      <div class="flex items-center justify-between mb-lg">
        <div class="flex items-center gap-md">
          <span class="badge badge-critical">12 Unresolved</span>
          <span class="badge badge-warning">8 Triaged</span>
          <span class="badge badge-healthy">27 Resolved</span>
        </div>
        <div class="flex gap-sm">
          <button class="btn btn-ghost"><i class="fas fa-filter"></i> Filter</button>
          <button class="btn btn-ghost"><i class="fas fa-sort-amount-down"></i> Most Frequent</button>
        </div>
      </div>

      <!-- Error Groups -->
      <div style="display:flex;flex-direction:column;gap:var(--space-md)">
        <!-- Error Group 1: Critical -->
        <div class="card error-group-card" style="border-left:3px solid var(--accent-red)">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between mb-sm">
              <div class="flex items-center gap-md">
                <span class="dot dot-critical" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem;font-family:'JetBrains Mono',monospace">NullPointerException</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">com.demo.inventory.StockService.checkAvailability(StockService.java:142)</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="error-count-badge">2,847</span>
                <span class="badge badge-critical">New</span>
              </div>
            </div>
            <div class="flex items-center gap-lg text-xs text-tertiary">
              <span><i class="fas fa-layer-group"></i> inventory-svc</span>
              <span><i class="fas fa-users"></i> ~340 users affected</span>
              <span><i class="fas fa-clock"></i> First: 45 min ago</span>
              <span><i class="fas fa-redo"></i> Last: 12s ago</span>
              <span class="error-sparkline">\u2581\u2582\u2583\u2585\u2587\u2588\u2588\u2588\u2588\u2588</span>
            </div>
          </div>
        </div>

        <!-- Error Group 2 -->
        <div class="card error-group-card" style="border-left:3px solid var(--accent-red)">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between mb-sm">
              <div class="flex items-center gap-md">
                <span class="dot dot-critical" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem;font-family:'JetBrains Mono',monospace">ConnectionTimeoutException</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">com.demo.payment.client.StripeClient.charge(StripeClient.java:89)</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="error-count-badge">456</span>
                <span class="badge badge-warning">Triaged</span>
              </div>
            </div>
            <div class="flex items-center gap-lg text-xs text-tertiary">
              <span><i class="fas fa-layer-group"></i> payment-gateway</span>
              <span><i class="fas fa-users"></i> ~120 users affected</span>
              <span><i class="fas fa-clock"></i> First: 2 hours ago</span>
              <span><i class="fas fa-redo"></i> Last: 3 min ago</span>
              <span class="error-sparkline">\u2583\u2585\u2587\u2585\u2583\u2582\u2581\u2581\u2582\u2583</span>
            </div>
          </div>
        </div>

        <!-- Error Group 3 -->
        <div class="card error-group-card" style="border-left:3px solid var(--accent-yellow)">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between mb-sm">
              <div class="flex items-center gap-md">
                <span class="dot dot-warning" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem;font-family:'JetBrains Mono',monospace">SlowQueryWarning</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">com.demo.order.repository.OrderDAO.findByUserId(OrderDAO.java:67) \u2014 query > 2000ms</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="error-count-badge" style="background:rgba(210,153,34,0.15);color:var(--accent-yellow)">89</span>
                <span class="badge badge-warning">Triaged</span>
              </div>
            </div>
            <div class="flex items-center gap-lg text-xs text-tertiary">
              <span><i class="fas fa-layer-group"></i> order-service</span>
              <span><i class="fas fa-users"></i> ~45 users affected</span>
              <span><i class="fas fa-clock"></i> First: 5 hours ago</span>
              <span><i class="fas fa-redo"></i> Last: 8 min ago</span>
              <span class="error-sparkline">\u2581\u2581\u2582\u2582\u2583\u2582\u2582\u2581\u2581\u2582</span>
            </div>
          </div>
        </div>

        <!-- Error Group 4: Resolved -->
        <div class="card error-group-card" style="border-left:3px solid var(--accent-green);opacity:0.7">
          <div class="card-body" style="padding:var(--space-md) var(--space-lg)">
            <div class="flex items-center justify-between mb-sm">
              <div class="flex items-center gap-md">
                <span class="dot dot-healthy" style="width:8px;height:8px"></span>
                <div>
                  <div style="font-weight:600;font-size:0.85rem;font-family:'JetBrains Mono',monospace;text-decoration:line-through;opacity:0.7">IllegalStateException</div>
                  <div class="text-xs text-secondary" style="margin-top:2px">com.demo.auth.TokenValidator.validate(TokenValidator.java:23)</div>
                </div>
              </div>
              <div class="flex items-center gap-md">
                <span class="error-count-badge" style="background:rgba(63,185,80,0.15);color:var(--accent-green)">0</span>
                <span class="badge badge-healthy">Resolved</span>
              </div>
            </div>
            <div class="flex items-center gap-lg text-xs text-tertiary">
              <span><i class="fas fa-layer-group"></i> user-auth</span>
              <span><i class="fas fa-check-circle" style="color:var(--accent-green)"></i> Fixed in v3.1.0</span>
              <span><i class="fas fa-clock"></i> Resolved 2 days ago</span>
            </div>
          </div>
        </div>
      </div>
    `;
  }
});
