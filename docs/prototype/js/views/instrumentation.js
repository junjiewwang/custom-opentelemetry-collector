/**
 * Instrumentation Page View
 * Master-Detail layout with Rule List + Detail panel (4 tabs: Targets, Runtime, Config, Audit)
 * Includes New Rule Modal
 */
ViewRouter.register('instrumentation', {
  render: function(container) {
    container.innerHTML = `
      <!-- Page Header -->
      <div class="flex items-center justify-between mb-lg">
        <div class="flex items-center gap-md">
          <span class="badge badge-info">12 active rules</span>
          <span class="badge badge-neutral">3 paused</span>
          <span class="badge badge-critical">1 failed</span>
        </div>
        <button class="btn btn-primary" onclick="openNewRuleModal()"><i class="fas fa-plus"></i> New Rule</button>
      </div>

      <!-- Master-Detail Layout -->
      <div class="grid-2-1" style="grid-template-columns:320px 1fr;gap:var(--space-lg)">
        <!-- Left: Rule List -->
        <div class="card" style="max-height:calc(100vh - 180px);overflow-y:auto">
          <div class="card-header" style="position:sticky;top:0;z-index:2;background:var(--bg-secondary)">
            <span class="card-title">Rules</span>
            <span class="text-xs text-tertiary">16 total</span>
          </div>
          <div style="padding:var(--space-sm) var(--space-md);border-bottom:1px solid var(--border-default);display:flex;gap:var(--space-xs)">
            <button class="btn btn-ghost text-xs" style="background:var(--accent-blue-dim);color:var(--accent-blue);border-radius:var(--radius-sm);padding:2px 8px">All</button>
            <button class="btn btn-ghost text-xs" style="padding:2px 8px">Active</button>
            <button class="btn btn-ghost text-xs" style="padding:2px 8px">Paused</button>
            <button class="btn btn-ghost text-xs" style="padding:2px 8px">Failed</button>
          </div>
          <div style="display:flex;flex-direction:column">
            <div class="inst-rule-item inst-rule-item--active" onclick="selectInstrumentationRule(0)">
              <div class="flex items-center justify-between">
                <span class="font-mono text-xs" style="color:var(--accent-blue)">OrderService.submit</span>
                <span class="badge badge-healthy" style="font-size:0.6rem">active</span>
              </div>
              <div class="text-xs text-tertiary" style="margin-top:3px">order-service \u00b7 trace \u00b7 4/4 applied</div>
            </div>
            <div class="inst-rule-item" onclick="selectInstrumentationRule(1)">
              <div class="flex items-center justify-between">
                <span class="font-mono text-xs" style="color:var(--accent-blue)">PaymentGateway.charge</span>
                <span class="badge badge-warning" style="font-size:0.6rem">partial</span>
              </div>
              <div class="text-xs text-tertiary" style="margin-top:3px">payment-gateway \u00b7 trace \u00b7 2/3 applied</div>
            </div>
            <div class="inst-rule-item" onclick="selectInstrumentationRule(2)">
              <div class="flex items-center justify-between">
                <span class="font-mono text-xs" style="color:var(--accent-blue)">InventoryService.query</span>
                <span class="badge badge-critical" style="font-size:0.6rem">failed</span>
              </div>
              <div class="text-xs text-tertiary" style="margin-top:3px">inventory-svc \u00b7 metric \u00b7 0/2 offline</div>
            </div>
            <div class="inst-rule-item" onclick="selectInstrumentationRule(3)">
              <div class="flex items-center justify-between">
                <span class="font-mono text-xs" style="color:var(--accent-blue)">AuthService.validate</span>
                <span class="badge badge-healthy" style="font-size:0.6rem">active</span>
              </div>
              <div class="text-xs text-tertiary" style="margin-top:3px">user-auth \u00b7 trace \u00b7 2/2 applied</div>
            </div>
            <div class="inst-rule-item" onclick="selectInstrumentationRule(4)">
              <div class="flex items-center justify-between">
                <span class="font-mono text-xs" style="color:var(--accent-blue)">CartService.addItem</span>
                <span class="badge badge-neutral" style="font-size:0.6rem">paused</span>
              </div>
              <div class="text-xs text-tertiary" style="margin-top:3px">cart-service \u00b7 trace \u00b7 3/3 removed</div>
            </div>
          </div>
        </div>

        <!-- Right: Rule Detail -->
        <div style="display:flex;flex-direction:column;gap:var(--space-lg)" id="inst-detail-panel">
          <!-- Detail Header -->
          <div class="card">
            <div class="card-body" style="padding:var(--space-lg)">
              <div class="flex items-center justify-between mb-md">
                <div class="flex items-center gap-md">
                  <div style="width:36px;height:36px;border-radius:var(--radius-md);background:var(--accent-blue-dim);display:flex;align-items:center;justify-content:center">
                    <i class="fas fa-microscope" style="color:var(--accent-blue);font-size:14px"></i>
                  </div>
                  <div>
                    <div style="font-weight:600;font-size:0.95rem" class="font-mono">OrderService.submit</div>
                    <div class="text-xs text-tertiary" style="margin-top:2px">Capture execution trace with args & return value</div>
                  </div>
                </div>
                <div class="flex gap-sm">
                  <button class="btn btn-ghost text-xs"><i class="fas fa-sync-alt"></i> Refresh</button>
                  <button class="btn btn-ghost text-xs" style="color:var(--accent-yellow)"><i class="fas fa-pause"></i> Pause</button>
                  <button class="btn btn-ghost text-xs" style="color:var(--accent-red)"><i class="fas fa-trash"></i></button>
                </div>
              </div>
              <div style="display:grid;grid-template-columns:repeat(5,1fr);gap:var(--space-md);padding-top:var(--space-md);border-top:1px solid var(--border-default)">
                <div><div class="text-xs text-tertiary">Service</div><div class="font-mono text-sm" style="margin-top:3px;color:var(--text-primary)">order-service</div></div>
                <div><div class="text-xs text-tertiary">Class</div><div class="font-mono text-sm" style="margin-top:3px;color:var(--text-primary)">com.demo.OrderService</div></div>
                <div><div class="text-xs text-tertiary">Method</div><div class="font-mono text-sm" style="margin-top:3px;color:var(--text-primary)">submit</div></div>
                <div><div class="text-xs text-tertiary">Type</div><div style="margin-top:3px"><span class="badge badge-info">trace</span></div></div>
                <div><div class="text-xs text-tertiary">Desired State</div><div style="margin-top:3px"><span class="badge badge-healthy"><span class="dot dot-healthy"></span>active</span></div></div>
              </div>
            </div>
          </div>

          <!-- Health Summary Bar -->
          <div class="card" style="padding:var(--space-lg)">
            <div class="flex items-center justify-between" style="margin-bottom:var(--space-md)">
              <div class="flex items-center gap-sm">
                <i class="fas fa-check-circle" style="color:var(--accent-green)"></i>
                <span style="font-weight:600;font-size:0.85rem;color:var(--accent-green)">Healthy</span>
                <span class="text-xs text-tertiary" style="margin-left:var(--space-sm)">4/4 active applied</span>
                <span class="text-xs" style="color:var(--text-quaternary);margin-left:4px">(+5 inactive)</span>
              </div>
              <div class="flex items-center gap-md">
                <span class="badge badge-info" style="font-weight:700">100% coverage</span>
                <span class="text-xs text-tertiary"><i class="fas fa-clock" style="margin-right:4px"></i>snapshot 30s ago</span>
              </div>
            </div>
            <div style="width:100%;height:10px;border-radius:6px;background:var(--bg-tertiary);overflow:hidden;display:flex">
              <div style="width:100%;height:100%;background:var(--accent-green);transition:width .5s"></div>
            </div>
            <div class="flex items-center gap-md" style="margin-top:var(--space-sm)">
              <span class="flex items-center gap-xs text-xs text-tertiary"><span style="width:8px;height:8px;border-radius:50%;background:var(--accent-green);display:inline-block"></span>Applied (4)</span>
              <span class="flex items-center gap-xs text-xs text-tertiary"><span style="width:8px;height:8px;border-radius:50%;background:var(--accent-blue);display:inline-block"></span>Running (0)</span>
              <span class="flex items-center gap-xs text-xs text-tertiary"><span style="width:8px;height:8px;border-radius:50%;background:var(--accent-red);display:inline-block"></span>Failed (0)</span>
              <span class="flex items-center gap-xs text-xs text-tertiary" style="margin-left:auto"><span style="width:8px;height:8px;border-radius:50%;background:var(--border-default);display:inline-block"></span>Inactive (5)</span>
            </div>
          </div>

          <!-- Detail Tabs -->
          <div class="card" style="overflow:hidden">
            <div style="display:flex;border-bottom:1px solid var(--border-default);background:var(--bg-tertiary)" id="inst-tab-bar">
              <button class="inst-tab inst-tab--active" onclick="switchInstTab('targets')" data-tab="targets">
                <i class="fas fa-crosshairs" style="font-size:10px"></i> Targets <span class="inst-tab-badge">4</span>
              </button>
              <button class="inst-tab" onclick="switchInstTab('runtime')" data-tab="runtime">
                <i class="fas fa-satellite-dish" style="font-size:10px"></i> Runtime <span class="inst-tab-badge">4</span>
              </button>
              <button class="inst-tab" onclick="switchInstTab('config')" data-tab="config">
                <i class="fas fa-sliders-h" style="font-size:10px"></i> Config & Op
              </button>
              <button class="inst-tab" onclick="switchInstTab('audit')" data-tab="audit">
                <i class="fas fa-history" style="font-size:10px"></i> Audit <span class="inst-tab-badge">5</span>
              </button>
            </div>

            <!-- Tab Panel: Targets -->
            <div class="inst-tab-panel" id="inst-panel-targets" style="display:block">
              <div class="table-container">
                <table>
                  <thead><tr><th>Agent / Host</th><th>State</th><th>Task</th><th>Error</th><th>Updated</th></tr></thead>
                  <tbody>
                    <tr>
                      <td><span class="font-mono text-xs" style="color:var(--accent-blue)">agent-pod-7f4d8</span><br><span class="text-xs text-tertiary">10.0.1.12 \u00b7 order-pod-7f4d8</span></td>
                      <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>applied</span></td>
                      <td><span class="text-xs text-secondary">dynamic_instrument</span><br><span class="text-xs text-tertiary font-mono">task-a1b2c3</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td class="text-xs text-secondary">2 min ago</td>
                    </tr>
                    <tr>
                      <td><span class="font-mono text-xs" style="color:var(--accent-blue)">agent-pod-a2b3c</span><br><span class="text-xs text-tertiary">10.0.1.13 \u00b7 order-pod-a2b3c</span></td>
                      <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>applied</span></td>
                      <td><span class="text-xs text-secondary">dynamic_instrument</span><br><span class="text-xs text-tertiary font-mono">task-d4e5f6</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td class="text-xs text-secondary">2 min ago</td>
                    </tr>
                    <tr>
                      <td><span class="font-mono text-xs" style="color:var(--accent-blue)">agent-pod-d4e5f</span><br><span class="text-xs text-tertiary">10.0.1.14 \u00b7 order-pod-d4e5f</span></td>
                      <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>applied</span></td>
                      <td><span class="text-xs text-secondary">dynamic_instrument</span><br><span class="text-xs text-tertiary font-mono">task-g7h8i9</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td class="text-xs text-secondary">5 min ago</td>
                    </tr>
                    <tr>
                      <td><span class="font-mono text-xs" style="color:var(--accent-blue)">agent-pod-g7h8i</span><br><span class="text-xs text-tertiary">10.0.1.15 \u00b7 order-pod-g7h8i</span></td>
                      <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>applied</span></td>
                      <td><span class="text-xs text-secondary">dynamic_instrument</span><br><span class="text-xs text-tertiary font-mono">task-j1k2l3</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td class="text-xs text-secondary">5 min ago</td>
                    </tr>
                    <!-- Completed / Removed -->
                    <tr class="inst-offline-divider" onclick="toggleCompletedTargets()">
                      <td colspan="5">
                        <div class="flex items-center gap-sm" style="cursor:pointer">
                          <i class="fas fa-chevron-right inst-offline-chevron" id="inst-completed-chevron"></i>
                          <i class="fas fa-check-circle" style="color:var(--text-tertiary);font-size:11px"></i>
                          <span class="text-xs" style="color:var(--text-tertiary);font-weight:500">Completed / Removed (3)</span>
                          <span class="text-xs text-tertiary" style="margin-left:auto">click to expand</span>
                        </div>
                      </td>
                    </tr>
                    <tr class="inst-completed-row" style="display:none">
                      <td><span class="font-mono text-xs" style="color:var(--text-tertiary)">agent-pod-k3l4m</span><br><span class="text-xs text-tertiary">10.0.1.18 \u00b7 order-pod-k3l4m</span></td>
                      <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>removed</span></td>
                      <td><span class="text-xs text-tertiary">dynamic_uninstrument</span><br><span class="text-xs text-tertiary font-mono">task-r5s6t7</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td class="text-xs text-tertiary">1h ago</td>
                    </tr>
                    <tr class="inst-completed-row" style="display:none">
                      <td><span class="font-mono text-xs" style="color:var(--text-tertiary)">agent-pod-n5o6p</span><br><span class="text-xs text-tertiary">10.0.1.19 \u00b7 order-pod-n5o6p</span></td>
                      <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>removed</span></td>
                      <td><span class="text-xs text-tertiary">dynamic_uninstrument</span><br><span class="text-xs text-tertiary font-mono">task-u8v9w0</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td class="text-xs text-tertiary">1h ago</td>
                    </tr>
                    <!-- Offline / Expired -->
                    <tr class="inst-offline-divider" onclick="toggleOfflineTargets()">
                      <td colspan="5">
                        <div class="flex items-center gap-sm" style="cursor:pointer">
                          <i class="fas fa-chevron-right inst-offline-chevron" id="inst-offline-chevron"></i>
                          <i class="fas fa-plug-circle-xmark" style="color:var(--text-tertiary);font-size:11px"></i>
                          <span class="text-xs" style="color:var(--text-tertiary);font-weight:500">Offline / Expired (2)</span>
                          <span class="text-xs text-tertiary" style="margin-left:auto">click to expand</span>
                        </div>
                      </td>
                    </tr>
                    <tr class="inst-offline-row" style="display:none">
                      <td><span class="font-mono text-xs" style="color:var(--text-tertiary)">agent-pod-m4n5o</span><br><span class="text-xs text-tertiary">10.0.1.16 \u00b7 order-pod-m4n5o</span></td>
                      <td><span class="badge badge-neutral"><span class="dot" style="background:var(--text-tertiary)"></span>offline</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td><span class="text-xs text-tertiary">agent is offline</span></td>
                      <td class="text-xs text-tertiary">3 days ago</td>
                    </tr>
                    <tr class="inst-offline-row" style="display:none">
                      <td><span class="font-mono text-xs" style="color:var(--text-tertiary)">agent-pod-p6q7r</span><br><span class="text-xs text-tertiary">10.0.1.17 \u00b7 order-pod-p6q7r</span></td>
                      <td><span class="badge" style="background:var(--accent-yellow-dim);color:var(--accent-yellow)"><span class="dot dot-warning"></span>expired</span></td>
                      <td><span class="text-xs text-tertiary">\u2014</span></td>
                      <td><span class="text-xs text-tertiary">agent offline > 7 days</span></td>
                      <td class="text-xs text-tertiary">8 days ago</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>

            <!-- Tab Panel: Runtime -->
            <div class="inst-tab-panel" id="inst-panel-runtime" style="display:none">
              <div style="display:grid;grid-template-columns:repeat(4,1fr);gap:var(--space-sm);padding:var(--space-md);border-bottom:1px solid var(--border-default)">
                <div style="text-align:center;padding:var(--space-sm)"><div class="text-xs text-tertiary">Reachable</div><div class="font-mono" style="font-size:1.1rem;margin-top:4px">4</div></div>
                <div style="text-align:center;padding:var(--space-sm)"><div class="text-xs text-tertiary">Effective</div><div class="font-mono" style="font-size:1.1rem;margin-top:4px;color:var(--accent-green)">4</div></div>
                <div style="text-align:center;padding:var(--space-sm)"><div class="text-xs text-tertiary">Drifted</div><div class="font-mono" style="font-size:1.1rem;margin-top:4px">0</div></div>
                <div style="text-align:center;padding:var(--space-sm)"><div class="text-xs text-tertiary">Missing</div><div class="font-mono" style="font-size:1.1rem;margin-top:4px">0</div></div>
              </div>
              <div class="table-container">
                <table>
                  <thead><tr><th>Agent</th><th>Effective</th><th>Refresh</th><th>Drift</th><th>Diagnostic</th></tr></thead>
                  <tbody>
                    <tr><td><span class="font-mono text-xs">agent-pod-7f4d8</span></td><td><span class="badge badge-healthy"><i class="fas fa-check" style="font-size:8px"></i> yes</span></td><td><span class="badge badge-healthy">success</span></td><td><span class="text-xs text-tertiary">\u2014</span></td><td><span class="text-xs text-secondary">Instrumentation available from AGENT_PREMAIN</span></td></tr>
                    <tr><td><span class="font-mono text-xs">agent-pod-a2b3c</span></td><td><span class="badge badge-healthy"><i class="fas fa-check" style="font-size:8px"></i> yes</span></td><td><span class="badge badge-healthy">success</span></td><td><span class="text-xs text-tertiary">\u2014</span></td><td><span class="text-xs text-secondary">Instrumentation available from AGENT_PREMAIN</span></td></tr>
                    <tr><td><span class="font-mono text-xs">agent-pod-d4e5f</span></td><td><span class="badge badge-healthy"><i class="fas fa-check" style="font-size:8px"></i> yes</span></td><td><span class="badge badge-healthy">success</span></td><td><span class="text-xs text-tertiary">\u2014</span></td><td><span class="text-xs text-secondary">Instrumentation available from AGENT_PREMAIN</span></td></tr>
                    <tr><td><span class="font-mono text-xs">agent-pod-g7h8i</span></td><td><span class="badge badge-healthy"><i class="fas fa-check" style="font-size:8px"></i> yes</span></td><td><span class="badge badge-healthy">success</span></td><td><span class="text-xs text-tertiary">\u2014</span></td><td><span class="text-xs text-secondary">Instrumentation available from AGENT_PREMAIN</span></td></tr>
                  </tbody>
                </table>
              </div>
              <div style="border-top:1px solid var(--border-default);padding:var(--space-sm) var(--space-md)">
                <button onclick="toggleRuntimeOffline()" style="background:none;border:none;cursor:pointer;display:flex;align-items:center;gap:6px;color:var(--text-tertiary);font-size:0.75rem">
                  <i class="fas fa-chevron-right" id="runtime-offline-chevron" style="font-size:9px;transition:transform 0.2s"></i>
                  <span>Offline / Expired (2)</span>
                </button>
                <div id="runtime-offline-group" style="display:none;margin-top:var(--space-sm)">
                  <div class="table-container" style="opacity:0.55">
                    <table>
                      <thead><tr><th>Agent</th><th>Effective</th><th>Refresh</th><th>Drift</th><th>Diagnostic</th></tr></thead>
                      <tbody>
                        <tr><td><span class="font-mono text-xs">agent-pod-m4n5o</span></td><td><span class="badge badge-neutral"><i class="fas fa-minus" style="font-size:8px"></i> offline</span></td><td><span class="badge" style="background:var(--accent-yellow-dim);color:var(--accent-yellow)">skipped</span></td><td><span class="text-xs text-tertiary">\u2014</span></td><td><span class="text-xs text-tertiary">agent is offline</span></td></tr>
                        <tr><td><span class="font-mono text-xs">agent-pod-p6q7r</span></td><td><span class="badge badge-neutral"><i class="fas fa-minus" style="font-size:8px"></i> offline</span></td><td><span class="badge" style="background:var(--accent-yellow-dim);color:var(--accent-yellow)">skipped</span></td><td><span class="text-xs text-tertiary">\u2014</span></td><td><span class="text-xs text-tertiary">agent expired</span></td></tr>
                      </tbody>
                    </table>
                  </div>
                </div>
              </div>
            </div>

            <!-- Tab Panel: Config & Operation -->
            <div class="inst-tab-panel" id="inst-panel-config" style="display:none;padding:var(--space-lg)">
              <div style="display:grid;grid-template-columns:1fr 1fr;gap:var(--space-lg)">
                <div style="border:1px solid var(--border-default);border-radius:var(--radius-md);padding:var(--space-md)">
                  <div class="text-xs" style="font-weight:600;text-transform:uppercase;letter-spacing:0.05em;color:var(--text-secondary);margin-bottom:var(--space-md)"><i class="fas fa-cog" style="margin-right:6px;color:var(--text-tertiary)"></i>Rule Configuration</div>
                  <div style="display:flex;flex-direction:column;gap:var(--space-sm)">
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Class</span><span class="font-mono" style="color:var(--text-primary)">com.demo.OrderService</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Method</span><span class="font-mono" style="color:var(--text-primary)">submit</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Type</span><span style="color:var(--text-primary)">trace</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Scope</span><span style="color:var(--text-primary)">service</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Span Name</span><span class="font-mono" style="color:var(--text-primary)">OrderService/submit</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Capture Args</span><span style="color:var(--text-primary)">arg0, arg1</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Capture Return</span><span style="color:var(--text-primary)">yes</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Service</span><span style="color:var(--text-primary)">order-service</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Created</span><span style="color:var(--text-primary)">2026/05/20 14:30</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Updated</span><span style="color:var(--text-primary)">2026/05/22 09:15</span></div>
                  </div>
                </div>
                <div style="border:1px solid var(--border-default);border-radius:var(--radius-md);padding:var(--space-md)">
                  <div class="text-xs" style="font-weight:600;text-transform:uppercase;letter-spacing:0.05em;color:var(--text-secondary);margin-bottom:var(--space-md)"><i class="fas fa-play-circle" style="margin-right:6px;color:var(--text-tertiary)"></i>Last Operation</div>
                  <div style="display:flex;flex-direction:column;gap:var(--space-sm)">
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Type</span><span style="color:var(--text-primary)">apply</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Status</span><span class="badge badge-healthy" style="font-size:0.6rem">success</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Started</span><span style="color:var(--text-primary)">2026/05/22 09:15</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Completed</span><span style="color:var(--text-primary)">2026/05/22 09:15</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Total</span><span style="color:var(--text-primary)">4</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Applied</span><span style="color:var(--accent-green)">4</span></div>
                    <div class="flex" style="font-size:0.75rem"><span style="width:100px;color:var(--text-tertiary);flex-shrink:0">Failed</span><span style="color:var(--text-primary)">0</span></div>
                  </div>
                </div>
              </div>
            </div>

            <!-- Tab Panel: Audit -->
            <div class="inst-tab-panel" id="inst-panel-audit" style="display:none">
              <div style="max-height:320px;overflow-y:auto">
                <div class="inst-audit-list">
                  <div class="inst-audit-item">
                    <div class="inst-audit-dot" style="background:var(--accent-green)"></div>
                    <div class="inst-audit-body">
                      <div class="flex items-center gap-sm"><span class="badge badge-healthy" style="font-size:0.6rem">target_discovered</span><span class="font-mono text-xs" style="color:var(--text-secondary)">agent-pod-g7h8i</span></div>
                      <div class="text-xs text-tertiary" style="margin-top:2px">discovered new service instance \u00b7 reconcile \u00b7 task-j1k2l3</div>
                    </div>
                    <span class="text-xs text-tertiary" style="white-space:nowrap">5 min ago</span>
                  </div>
                  <div class="inst-audit-item">
                    <div class="inst-audit-dot" style="background:var(--accent-green)"></div>
                    <div class="inst-audit-body">
                      <div class="flex items-center gap-sm"><span class="badge badge-healthy" style="font-size:0.6rem">apply</span><span class="font-mono text-xs" style="color:var(--text-secondary)">agent-pod-7f4d8</span></div>
                      <div class="text-xs text-tertiary" style="margin-top:2px">task dispatched \u00b7 manual</div>
                    </div>
                    <span class="text-xs text-tertiary" style="white-space:nowrap">12 min ago</span>
                  </div>
                  <div class="inst-audit-item">
                    <div class="inst-audit-dot" style="background:var(--accent-green)"></div>
                    <div class="inst-audit-body">
                      <div class="flex items-center gap-sm"><span class="badge badge-healthy" style="font-size:0.6rem">apply</span><span class="font-mono text-xs" style="color:var(--text-secondary)">agent-pod-a2b3c</span></div>
                      <div class="text-xs text-tertiary" style="margin-top:2px">task dispatched \u00b7 manual</div>
                    </div>
                    <span class="text-xs text-tertiary" style="white-space:nowrap">12 min ago</span>
                  </div>
                  <div class="inst-audit-item">
                    <div class="inst-audit-dot" style="background:var(--text-tertiary)"></div>
                    <div class="inst-audit-body">
                      <div class="flex items-center gap-sm"><span class="badge badge-neutral" style="font-size:0.6rem">apply \u00b7 skipped</span><span class="font-mono text-xs" style="color:var(--text-tertiary)">agent-pod-m4n5o</span></div>
                      <div class="text-xs text-tertiary" style="margin-top:2px">agent is offline \u00b7 reconcile</div>
                    </div>
                    <span class="text-xs text-tertiary" style="white-space:nowrap">3 days ago</span>
                  </div>
                  <div class="inst-audit-item">
                    <div class="inst-audit-dot" style="background:var(--accent-yellow)"></div>
                    <div class="inst-audit-body">
                      <div class="flex items-center gap-sm"><span class="badge badge-warning" style="font-size:0.6rem">target_pruned</span><span class="font-mono text-xs" style="color:var(--text-secondary)">agent-pod-x9y0z</span></div>
                      <div class="text-xs text-tertiary" style="margin-top:2px">target left current service scope \u00b7 reconcile</div>
                    </div>
                    <span class="text-xs text-tertiary" style="white-space:nowrap">1 day ago</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- New Rule Modal -->
      <div class="modal-overlay" id="new-rule-modal">
        <div class="modal">
          <div class="modal-header">
            <span class="modal-title"><i class="fas fa-microscope" style="color:var(--accent-purple)"></i> Create Instrumentation Rule</span>
            <button class="modal-close" onclick="closeNewRuleModal()"><i class="fas fa-times"></i></button>
          </div>
          <div class="modal-steps">
            <div class="modal-step modal-step--active" data-step="1"><span class="modal-step-num">1</span><span>Basic Info</span></div>
            <div class="modal-step-sep"></div>
            <div class="modal-step" data-step="2"><span class="modal-step-num">2</span><span>Target</span></div>
            <div class="modal-step-sep"></div>
            <div class="modal-step" data-step="3"><span class="modal-step-num">3</span><span>Capture</span></div>
          </div>
          <div class="modal-body">
            <div class="modal-section modal-section--active" id="modal-step-1">
              <div class="form-group"><label class="form-label">Rule Name</label><input class="form-input" type="text" placeholder="e.g. OrderService.submit" id="rule-name-input"><div class="form-hint">A descriptive name to identify this rule</div></div>
              <div class="form-group"><label class="form-label">Description</label><input class="form-input" type="text" placeholder="e.g. Capture args & return value for debugging"></div>
              <div class="form-group"><label class="form-label">Rule Type</label><div class="segmented-control" id="rule-type-seg"><div class="segment segment--active" data-value="trace" onclick="selectSegment(this)"><i class="fas fa-route"></i> Trace</div><div class="segment" data-value="metric" onclick="selectSegment(this)"><i class="fas fa-chart-bar"></i> Metric</div><div class="segment" data-value="log" onclick="selectSegment(this)"><i class="fas fa-file-alt"></i> Log</div></div></div>
              <div class="form-group"><label class="form-label">Target Service</label><select class="form-select"><option value="">Select a service...</option><option value="order-service">order-service</option><option value="payment-gateway">payment-gateway</option><option value="inventory-svc">inventory-svc</option><option value="user-auth">user-auth</option><option value="notification-svc">notification-svc</option></select></div>
            </div>
            <div class="modal-section" id="modal-step-2">
              <div class="form-group"><label class="form-label">Class Pattern</label><input class="form-input form-input--mono" type="text" placeholder="com.demo.order.OrderService" id="class-pattern-input"><div class="form-hint">Fully qualified class name (supports * wildcard)</div></div>
              <div class="form-group"><label class="form-label">Method Pattern</label><input class="form-input form-input--mono" type="text" placeholder="submit*" id="method-pattern-input"><div class="form-hint">Method name or pattern (supports * wildcard)</div></div>
              <div class="match-preview" id="match-preview"><i class="fas fa-info-circle"></i><span>Will match <strong>4 instances</strong> in <strong>order-service</strong></span></div>
            </div>
            <div class="modal-section" id="modal-step-3">
              <div class="form-group"><label class="form-label">Capture Options</label><div class="checkbox-group"><div class="checkbox-item checkbox-item--checked" onclick="toggleCheckbox(this)"><div class="checkbox-box"><i class="fas fa-check"></i></div><span>Arguments</span></div><div class="checkbox-item checkbox-item--checked" onclick="toggleCheckbox(this)"><div class="checkbox-box"><i class="fas fa-check"></i></div><span>Return Value</span></div><div class="checkbox-item" onclick="toggleCheckbox(this)"><div class="checkbox-box"><i class="fas fa-check"></i></div><span>Exception</span></div><div class="checkbox-item" onclick="toggleCheckbox(this)"><div class="checkbox-box"><i class="fas fa-check"></i></div><span>Execution Time</span></div></div></div>
              <div class="form-group"><label class="form-label">Sampling Rate</label><div class="form-row"><input class="form-input form-input--mono" type="text" value="100" style="width:80px"><span class="text-sm text-secondary" style="align-self:center">% of requests</span></div></div>
              <div class="card" style="margin-top:var(--space-lg)"><div class="card-header"><span class="card-title text-xs"><i class="fas fa-eye" style="color:var(--accent-blue)"></i> Rule Summary</span></div><div class="card-body" style="padding:var(--space-md) var(--space-lg)"><div style="display:grid;grid-template-columns:1fr 1fr;gap:var(--space-sm)"><div><span class="text-xs text-tertiary">Name:</span> <span class="font-mono text-sm" id="summary-name">\u2014</span></div><div><span class="text-xs text-tertiary">Type:</span> <span class="badge badge-info text-xs" id="summary-type">trace</span></div><div><span class="text-xs text-tertiary">Class:</span> <span class="font-mono text-sm" id="summary-class">\u2014</span></div><div><span class="text-xs text-tertiary">Method:</span> <span class="font-mono text-sm" id="summary-method">\u2014</span></div></div></div></div>
            </div>
          </div>
          <div class="modal-footer">
            <div class="modal-footer-left"><button class="btn btn-ghost" id="modal-back-btn" onclick="modalPrevStep()" style="display:none"><i class="fas fa-arrow-left"></i> Back</button></div>
            <div class="modal-footer-right"><button class="btn btn-ghost" onclick="closeNewRuleModal()">Cancel</button><button class="btn btn-primary" id="modal-next-btn" onclick="modalNextStep()"><span id="modal-next-label">Next</span> <i class="fas fa-arrow-right" id="modal-next-icon"></i></button></div>
          </div>
        </div>
      </div>
    `;
  },

  init: function(container) {
    // Expose instrumentation-related global functions
    var modalCurrentStep = 1;
    var MODAL_TOTAL_STEPS = 3;

    window.selectInstrumentationRule = function(index) {
      document.querySelectorAll('.inst-rule-item').forEach(function(item, i) {
        item.classList.toggle('inst-rule-item--active', i === index);
      });
    };

    window.switchInstTab = function(tabKey) {
      document.querySelectorAll('#inst-tab-bar .inst-tab').forEach(function(btn) {
        btn.classList.toggle('inst-tab--active', btn.dataset.tab === tabKey);
      });
      document.querySelectorAll('.inst-tab-panel').forEach(function(panel) {
        panel.style.display = panel.id === 'inst-panel-' + tabKey ? 'block' : 'none';
      });
    };

    window.toggleOfflineTargets = function() {
      var rows = document.querySelectorAll('.inst-offline-row');
      var chevron = document.getElementById('inst-offline-chevron');
      var isHidden = rows[0] && rows[0].style.display === 'none';
      rows.forEach(function(row) { row.style.display = isHidden ? '' : 'none'; });
      if (chevron) chevron.classList.toggle('inst-offline-chevron--expanded', isHidden);
    };

    window.toggleCompletedTargets = function() {
      var rows = document.querySelectorAll('.inst-completed-row');
      var chevron = document.getElementById('inst-completed-chevron');
      var isHidden = rows[0] && rows[0].style.display === 'none';
      rows.forEach(function(row) { row.style.display = isHidden ? '' : 'none'; });
      if (chevron) chevron.classList.toggle('inst-offline-chevron--expanded', isHidden);
    };

    window.toggleRuntimeOffline = function() {
      var group = document.getElementById('runtime-offline-group');
      var chevron = document.getElementById('runtime-offline-chevron');
      if (!group) return;
      var isHidden = group.style.display === 'none';
      group.style.display = isHidden ? '' : 'none';
      if (chevron) chevron.style.transform = isHidden ? 'rotate(90deg)' : '';
    };

    window.openNewRuleModal = function() {
      modalCurrentStep = 1;
      updateModalUI();
      document.getElementById('new-rule-modal').classList.add('modal-overlay--open');
      setTimeout(function() { var input = document.getElementById('rule-name-input'); if (input) input.focus(); }, 300);
    };

    window.closeNewRuleModal = function() {
      document.getElementById('new-rule-modal').classList.remove('modal-overlay--open');
    };

    window.modalNextStep = function() {
      if (modalCurrentStep < MODAL_TOTAL_STEPS) { modalCurrentStep++; updateModalUI(); }
      else { createRule(); }
    };

    window.modalPrevStep = function() {
      if (modalCurrentStep > 1) { modalCurrentStep--; updateModalUI(); }
    };

    window.selectSegment = function(el) {
      el.parentElement.querySelectorAll('.segment').forEach(function(s) { s.classList.remove('segment--active'); });
      el.classList.add('segment--active');
    };

    window.toggleCheckbox = function(el) { el.classList.toggle('checkbox-item--checked'); };

    function updateModalUI() {
      for (var i = 1; i <= MODAL_TOTAL_STEPS; i++) {
        var section = document.getElementById('modal-step-' + i);
        if (section) section.classList.toggle('modal-section--active', i === modalCurrentStep);
      }
      document.querySelectorAll('.modal-step').forEach(function(step) {
        var stepNum = parseInt(step.dataset.step);
        step.classList.remove('modal-step--active', 'modal-step--done');
        if (stepNum === modalCurrentStep) step.classList.add('modal-step--active');
        else if (stepNum < modalCurrentStep) step.classList.add('modal-step--done');
      });
      var backBtn = document.getElementById('modal-back-btn');
      var nextLabel = document.getElementById('modal-next-label');
      var nextIcon = document.getElementById('modal-next-icon');
      backBtn.style.display = modalCurrentStep > 1 ? '' : 'none';
      if (modalCurrentStep === MODAL_TOTAL_STEPS) { nextLabel.textContent = 'Create Rule'; nextIcon.className = 'fas fa-check'; }
      else { nextLabel.textContent = 'Next'; nextIcon.className = 'fas fa-arrow-right'; }
      if (modalCurrentStep === MODAL_TOTAL_STEPS) updateSummary();
    }

    function updateSummary() {
      var name = document.getElementById('rule-name-input');
      var classVal = document.getElementById('class-pattern-input');
      var methodVal = document.getElementById('method-pattern-input');
      var typeActive = document.querySelector('#rule-type-seg .segment--active');
      document.getElementById('summary-name').textContent = (name && name.value) || '\u2014';
      document.getElementById('summary-class').textContent = (classVal && classVal.value) || '\u2014';
      document.getElementById('summary-method').textContent = (methodVal && methodVal.value) || '\u2014';
      document.getElementById('summary-type').textContent = typeActive ? typeActive.dataset.value : 'trace';
    }

    function createRule() {
      var nextBtn = document.getElementById('modal-next-btn');
      nextBtn.innerHTML = '<i class="fas fa-spinner fa-spin"></i> Creating...';
      nextBtn.disabled = true;
      setTimeout(function() {
        closeNewRuleModal();
        nextBtn.innerHTML = '<span id="modal-next-label">Next</span> <i class="fas fa-arrow-right" id="modal-next-icon"></i>';
        nextBtn.disabled = false;
      }, 800);
    }

    // Close modal on overlay click
    var modal = document.getElementById('new-rule-modal');
    if (modal) {
      modal.addEventListener('click', function(e) { if (e.target === e.currentTarget) closeNewRuleModal(); });
    }
  }
});
