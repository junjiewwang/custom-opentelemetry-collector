/**
 * Traces Page View
 * Includes: Filter Bar, Scatter Plot / Heatmap visualization, Trace List, Trace Drawer template
 */
ViewRouter.register('traces', {
  render: function(container) {
    container.innerHTML = `
      <!-- Filter Bar -->
      <div class="filter-bar">
        <i class="fas fa-search" style="color:var(--text-tertiary)"></i>
        <div class="filter-tag">service = order-service <i class="fas fa-times" style="font-size:0.6rem;cursor:pointer"></i></div>
        <div class="filter-tag">status = error <i class="fas fa-times" style="font-size:0.6rem;cursor:pointer"></i></div>
        <input class="filter-input" placeholder="Add filter... (e.g. duration > 500ms, operation = /api/orders)">
        <button class="btn btn-primary"><i class="fas fa-search"></i> Search</button>
      </div>

      <!-- Visualization: Tab-switched Scatter Plot / Heatmap -->
      <div class="card mb-lg">
        <div class="card-header">
          <div style="display:flex;align-items:center;gap:0">
            <div class="trace-viz-tab active" data-viz="scatter" onclick="switchTraceViz(this)">
              <i class="fas fa-braille" style="font-size:0.7rem"></i> Scatter Plot
            </div>
            <div class="trace-viz-tab" data-viz="heatmap" onclick="switchTraceViz(this)">
              <i class="fas fa-fire" style="font-size:0.7rem"></i> Heatmap
            </div>
          </div>
          <span class="text-xs text-tertiary">1,234 traces · last 15 min</span>
        </div>
        <!-- Scatter Plot Panel -->
        <div class="trace-viz-panel active" id="viz-scatter">
          <div style="height:150px;position:relative;overflow:hidden;padding:8px">
            <svg width="100%" height="100%" viewBox="0 0 800 130">
              <line x1="0" y1="32" x2="800" y2="32" stroke="#21262d" stroke-width="1"/>
              <line x1="0" y1="65" x2="800" y2="65" stroke="#21262d" stroke-width="1"/>
              <line x1="0" y1="98" x2="800" y2="98" stroke="#21262d" stroke-width="1"/>
              <!-- Normal points -->
              <circle cx="45" cy="92" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="82" cy="95" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="120" cy="89" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="155" cy="97" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="190" cy="85" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="230" cy="91" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="265" cy="93" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="300" cy="88" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="340" cy="90" r="4" fill="#58a6ff" opacity="0.6"/><circle cx="375" cy="94" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="410" cy="86" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="450" cy="92" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="485" cy="96" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="520" cy="84" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="555" cy="90" r="4" fill="#58a6ff" opacity="0.6"/><circle cx="590" cy="93" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="625" cy="88" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="665" cy="91" r="3" fill="#58a6ff" opacity="0.6"/>
              <circle cx="700" cy="85" r="3" fill="#58a6ff" opacity="0.6"/><circle cx="735" cy="94" r="3" fill="#58a6ff" opacity="0.6"/>
              <!-- Slow points -->
              <circle cx="195" cy="42" r="4" fill="#d29922" opacity="0.8"/><circle cx="410" cy="35" r="5" fill="#d29922" opacity="0.8"/>
              <circle cx="590" cy="49" r="4" fill="#d29922" opacity="0.8"/>
              <!-- Error points -->
              <circle cx="310" cy="22" r="5" fill="#f85149" opacity="0.9"/><circle cx="520" cy="15" r="6" fill="#f85149" opacity="0.9"/>
              <circle cx="680" cy="27" r="5" fill="#f85149" opacity="0.9"/>
              <!-- Y axis labels -->
              <text x="2" y="101" fill="#6e7681" font-size="9" font-family="JetBrains Mono">50ms</text>
              <text x="2" y="68" fill="#6e7681" font-size="9" font-family="JetBrains Mono">200ms</text>
              <text x="2" y="35" fill="#6e7681" font-size="9" font-family="JetBrains Mono">500ms</text>
            </svg>
          </div>
        </div>
        <!-- Heatmap Panel -->
        <div class="trace-viz-panel" id="viz-heatmap">
          <div style="height:150px;position:relative;padding:var(--space-md)">
            <div style="position:absolute;left:12px;top:12px;bottom:32px;width:44px;display:flex;flex-direction:column;justify-content:space-between;padding:4px 0">
              <span class="text-xs font-mono text-tertiary">2s+</span>
              <span class="text-xs font-mono text-tertiary">1s</span>
              <span class="text-xs font-mono text-tertiary">500ms</span>
              <span class="text-xs font-mono text-tertiary">200ms</span>
              <span class="text-xs font-mono text-tertiary">50ms</span>
              <span class="text-xs font-mono text-tertiary">10ms</span>
            </div>
            <div style="margin-left:54px;height:120px">
              <svg width="100%" height="100%" viewBox="0 0 480 120" preserveAspectRatio="none">
                <rect x="0" y="0" width="12" height="20" fill="#f8514900"/><rect x="14" y="0" width="12" height="20" fill="#f8514900"/>
                <rect x="28" y="0" width="12" height="20" fill="#f8514910"/><rect x="42" y="0" width="12" height="20" fill="#f8514900"/>
                <rect x="56" y="0" width="12" height="20" fill="#f8514900"/><rect x="70" y="0" width="12" height="20" fill="#f8514920"/>
                <rect x="84" y="0" width="12" height="20" fill="#f8514900"/><rect x="98" y="0" width="12" height="20" fill="#f8514930"/>
                <rect x="112" y="0" width="12" height="20" fill="#f8514950"/><rect x="126" y="0" width="12" height="20" fill="#f8514960"/>
                <rect x="0" y="20" width="12" height="20" fill="#d2992210"/><rect x="14" y="20" width="12" height="20" fill="#d2992220"/>
                <rect x="28" y="20" width="12" height="20" fill="#d2992215"/><rect x="42" y="20" width="12" height="20" fill="#d2992230"/>
                <rect x="56" y="20" width="12" height="20" fill="#d2992240"/><rect x="70" y="20" width="12" height="20" fill="#d2992250"/>
                <rect x="84" y="20" width="12" height="20" fill="#d2992260"/><rect x="98" y="20" width="12" height="20" fill="#f8514970"/>
                <rect x="112" y="20" width="12" height="20" fill="#f8514980"/><rect x="126" y="20" width="12" height="20" fill="#d2992240"/>
                <rect x="0" y="40" width="12" height="20" fill="#d2992230"/><rect x="14" y="40" width="12" height="20" fill="#d2992240"/>
                <rect x="28" y="40" width="12" height="20" fill="#d2992235"/><rect x="42" y="40" width="12" height="20" fill="#d2992250"/>
                <rect x="56" y="40" width="12" height="20" fill="#d2992260"/><rect x="70" y="40" width="12" height="20" fill="#d2992270"/>
                <rect x="84" y="40" width="12" height="20" fill="#d2992280"/><rect x="98" y="40" width="12" height="20" fill="#d2992275"/>
                <rect x="112" y="40" width="12" height="20" fill="#d2992260"/><rect x="126" y="40" width="12" height="20" fill="#d2992250"/>
                <rect x="0" y="60" width="12" height="20" fill="#58a6ff50"/><rect x="14" y="60" width="12" height="20" fill="#58a6ff60"/>
                <rect x="28" y="60" width="12" height="20" fill="#58a6ff55"/><rect x="42" y="60" width="12" height="20" fill="#58a6ff65"/>
                <rect x="56" y="60" width="12" height="20" fill="#58a6ff70"/><rect x="70" y="60" width="12" height="20" fill="#58a6ff60"/>
                <rect x="84" y="60" width="12" height="20" fill="#58a6ff55"/><rect x="98" y="60" width="12" height="20" fill="#58a6ff65"/>
                <rect x="112" y="60" width="12" height="20" fill="#58a6ff50"/><rect x="126" y="60" width="12" height="20" fill="#58a6ff55"/>
                <rect x="0" y="80" width="12" height="20" fill="#3fb95080"/><rect x="14" y="80" width="12" height="20" fill="#3fb95090"/>
                <rect x="28" y="80" width="12" height="20" fill="#3fb95085"/><rect x="42" y="80" width="12" height="20" fill="#3fb95095"/>
                <rect x="56" y="80" width="12" height="20" fill="#3fb950a0"/><rect x="70" y="80" width="12" height="20" fill="#3fb95090"/>
                <rect x="84" y="80" width="12" height="20" fill="#3fb95085"/><rect x="98" y="80" width="12" height="20" fill="#3fb95070"/>
                <rect x="112" y="80" width="12" height="20" fill="#3fb95065"/><rect x="126" y="80" width="12" height="20" fill="#3fb95075"/>
                <rect x="0" y="100" width="12" height="20" fill="#3fb950b0"/><rect x="14" y="100" width="12" height="20" fill="#3fb950c0"/>
                <rect x="28" y="100" width="12" height="20" fill="#3fb950b5"/><rect x="42" y="100" width="12" height="20" fill="#3fb950c0"/>
                <rect x="56" y="100" width="12" height="20" fill="#3fb950d0"/><rect x="70" y="100" width="12" height="20" fill="#3fb950b0"/>
                <rect x="84" y="100" width="12" height="20" fill="#3fb950a0"/><rect x="98" y="100" width="12" height="20" fill="#3fb95095"/>
                <rect x="112" y="100" width="12" height="20" fill="#3fb95090"/><rect x="126" y="100" width="12" height="20" fill="#3fb950a0"/>
              </svg>
            </div>
            <div style="position:absolute;bottom:8px;left:54px;display:flex;align-items:center;gap:var(--space-md)">
              <span class="text-xs text-tertiary">Density:</span>
              <div style="display:flex;align-items:center;gap:2px">
                <div style="width:10px;height:6px;background:#3fb95030;border-radius:1px"></div>
                <div style="width:10px;height:6px;background:#3fb95060;border-radius:1px"></div>
                <div style="width:10px;height:6px;background:#3fb95090;border-radius:1px"></div>
                <div style="width:10px;height:6px;background:#d29922a0;border-radius:1px"></div>
                <div style="width:10px;height:6px;background:#f85149c0;border-radius:1px"></div>
              </div>
              <span class="text-xs text-tertiary">Low → High</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Trace List -->
      <div class="card">
        <div class="card-header">
          <span class="card-title">Traces</span>
          <div style="display:flex;align-items:center;gap:var(--space-md)">
            <span class="text-xs text-tertiary">Showing 10 of 1,234</span>
            <div class="btn btn-ghost" style="padding:2px 8px;font-size:0.7rem"><i class="fas fa-sort-amount-down"></i> Duration</div>
          </div>
        </div>
        <div class="table-container">
          <table>
            <thead>
              <tr>
                <th style="width:32px"></th>
                <th>Trace ID</th>
                <th>Root Service</th>
                <th>Operation</th>
                <th>Duration</th>
                <th>Spans</th>
                <th>Status</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody id="trace-list-tbody">
              <tr class="trace-row trace-row--selected" onclick="openTraceDrawer(this, 'abc123def456')" data-trace-id="abc123def456">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">abc123def456</td>
                <td>order-service</td>
                <td>POST /api/orders</td>
                <td class="font-mono" style="color:var(--accent-red)">1,240ms</td>
                <td>23</td>
                <td><span class="badge badge-critical"><span class="dot dot-critical"></span>Error</span></td>
                <td class="text-secondary">2 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'def789ghi012')" data-trace-id="def789ghi012">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">def789ghi012</td>
                <td>user-auth</td>
                <td>POST /auth/login</td>
                <td class="font-mono">89ms</td>
                <td>8</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>OK</span></td>
                <td class="text-secondary">3 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'ghi345jkl678')" data-trace-id="ghi345jkl678">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">ghi345jkl678</td>
                <td>payment-gateway</td>
                <td>POST /pay/charge</td>
                <td class="font-mono" style="color:var(--accent-yellow)">567ms</td>
                <td>15</td>
                <td><span class="badge badge-warning"><span class="dot dot-warning"></span>Slow</span></td>
                <td class="text-secondary">5 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'jkl901mno234')" data-trace-id="jkl901mno234">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">jkl901mno234</td>
                <td>order-service</td>
                <td>GET /api/orders/{id}</td>
                <td class="font-mono">45ms</td>
                <td>6</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>OK</span></td>
                <td class="text-secondary">6 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'mno567pqr890')" data-trace-id="mno567pqr890">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">mno567pqr890</td>
                <td>inventory-svc</td>
                <td>GET /api/stock</td>
                <td class="font-mono" style="color:var(--accent-red)">2,100ms</td>
                <td>31</td>
                <td><span class="badge badge-critical"><span class="dot dot-critical"></span>Error</span></td>
                <td class="text-secondary">8 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'pqr123stu456')" data-trace-id="pqr123stu456">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">pqr123stu456</td>
                <td>user-auth</td>
                <td>GET /auth/verify</td>
                <td class="font-mono">32ms</td>
                <td>4</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>OK</span></td>
                <td class="text-secondary">9 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'stu789vwx012')" data-trace-id="stu789vwx012">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">stu789vwx012</td>
                <td>payment-gateway</td>
                <td>POST /pay/refund</td>
                <td class="font-mono" style="color:var(--accent-yellow)">423ms</td>
                <td>11</td>
                <td><span class="badge badge-warning"><span class="dot dot-warning"></span>Slow</span></td>
                <td class="text-secondary">10 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'vwx345yza678')" data-trace-id="vwx345yza678">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">vwx345yza678</td>
                <td>notification-svc</td>
                <td>POST /notify/email</td>
                <td class="font-mono">78ms</td>
                <td>5</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>OK</span></td>
                <td class="text-secondary">11 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'yza901bcd234')" data-trace-id="yza901bcd234">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">yza901bcd234</td>
                <td>order-service</td>
                <td>PUT /api/orders/{id}/cancel</td>
                <td class="font-mono">156ms</td>
                <td>9</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>OK</span></td>
                <td class="text-secondary">12 min ago</td>
              </tr>
              <tr class="trace-row" onclick="openTraceDrawer(this, 'bcd567efg890')" data-trace-id="bcd567efg890">
                <td><i class="fas fa-chevron-right text-tertiary" style="font-size:0.6rem"></i></td>
                <td class="font-mono" style="color:var(--text-link)">bcd567efg890</td>
                <td>inventory-svc</td>
                <td>POST /api/stock/reserve</td>
                <td class="font-mono" style="color:var(--accent-yellow)">890ms</td>
                <td>18</td>
                <td><span class="badge badge-warning"><span class="dot dot-warning"></span>Slow</span></td>
                <td class="text-secondary">14 min ago</td>
              </tr>
            </tbody>
          </table>
        </div>
        <!-- Load More -->
        <div style="padding:var(--space-md) var(--space-lg);border-top:1px solid var(--border-muted);text-align:center">
          <button class="btn btn-ghost" style="width:100%"><i class="fas fa-ellipsis-h"></i> Load More Traces</button>
        </div>
      </div>

      <!-- Trace Drawer Overlay + Drawer (rendered with page, hidden by default) -->
      <div class="trace-drawer-overlay" id="trace-drawer-overlay" onclick="closeTraceDrawer()"></div>
      <div class="trace-drawer" id="trace-drawer">
        <!-- Top Bar -->
        <div class="trace-top-bar">
          <span class="trace-top-id" id="drawer-trace-id-v2">abc123def456</span>
          <span class="trace-top-badge trace-top-badge--err" id="drawer-status-badge">Error</span>
          <div class="trace-top-meta">
            <span class="trace-top-meta-label">Duration</span>
            <span class="trace-top-meta-value trace-top-meta-value--highlight" id="drawer-duration-v2">1,240ms</span>
          </div>
          <div class="trace-top-meta">
            <span class="trace-top-meta-label">Spans</span>
            <span class="trace-top-meta-value" id="drawer-spans-v2">23</span>
          </div>
          <div class="trace-top-meta">
            <span class="trace-top-meta-label">Services</span>
            <span class="trace-top-meta-value" id="drawer-services-v2">4</span>
          </div>
          <div class="trace-top-meta">
            <span class="trace-top-meta-label">Time</span>
            <span class="trace-top-meta-value" id="drawer-time-v2">10:23:45</span>
          </div>
          <div class="trace-top-links">
            <a href="#" class="trace-top-link trace-top-link--log"><i class="fas fa-file-alt"></i> Logs</a>
            <button class="trace-top-link trace-top-link--copy" onclick="navigator.clipboard.writeText(document.getElementById('drawer-trace-id-v2').textContent)"><i class="fas fa-copy"></i></button>
            <button class="btn btn-ghost" onclick="closeTraceDrawer()" style="padding:4px 8px;color:#94a3b8"><i class="fas fa-times"></i></button>
          </div>
        </div>
        <!-- Sub Bar -->
        <div class="trace-sub-bar">
          <span class="trace-sub-info">Root: <strong id="drawer-root-svc">order-service</strong></span>
          <span class="trace-sub-info">Operation: <strong class="font-mono" id="drawer-root-op">POST /api/orders</strong></span>
          <span class="trace-sub-info">Instance: <strong id="drawer-instance">order-pod-7x9f2</strong></span>
        </div>
        <!-- Body: Golden Ratio Split -->
        <div class="trace-drawer-body">
          <!-- Left: Waterfall -->
          <div class="trace-drawer-waterfall">
            <div class="tw-tree-header">
              <div class="tw-col-name">Span</div>
              <div class="tw-col-timeline">
                <div class="tw-time-ruler">
                  <span class="tw-tick" style="left:0%">0ms</span>
                  <span class="tw-tick" style="left:25%">310ms</span>
                  <span class="tw-tick" style="left:50%">620ms</span>
                  <span class="tw-tick" style="left:75%">930ms</span>
                  <span class="tw-tick" style="left:100%">1,240ms</span>
                </div>
              </div>
              <div class="tw-col-dur">Duration</div>
            </div>
            <div class="tw-tree-body" id="tw-tree-body">
              <div class="tw-span-row tw-span-row--selected" data-span-idx="0" onclick="selectSpanV2(this, 0)">
                <div class="tw-cell-name">
                  <button class="tw-toggle" data-expanded="true">\u25BC</button>
                  <span class="tw-dot tw-dot--err"></span>
                  <span class="tw-svc-tag">order-service</span>
                  <span class="tw-op-name">POST /api/orders</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar" style="left:0%;width:100%;background:var(--accent-blue)"></div></div>
                <div class="tw-cell-dur">1,240ms</div>
              </div>
              <div class="tw-span-row" data-span-idx="1" onclick="selectSpanV2(this, 1)">
                <div class="tw-cell-name" style="padding-left:24px">
                  <span class="tw-toggle-spacer"></span>
                  <span class="tw-dot tw-dot--ok"></span>
                  <span class="tw-svc-tag tw-svc--green">user-auth</span>
                  <span class="tw-op-name">validateToken</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar" style="left:1%;width:3%;background:var(--accent-green)"></div></div>
                <div class="tw-cell-dur">34ms</div>
              </div>
              <div class="tw-span-row tw-span-row--error" data-span-idx="2" onclick="selectSpanV2(this, 2)">
                <div class="tw-cell-name" style="padding-left:24px">
                  <button class="tw-toggle" data-expanded="true">\u25BC</button>
                  <span class="tw-dot tw-dot--err"></span>
                  <span class="tw-svc-tag tw-svc--orange">inventory-svc</span>
                  <span class="tw-op-name">checkStock</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar tw-bar--err" style="left:4%;width:66%;background:var(--accent-red)"></div></div>
                <div class="tw-cell-dur tw-dur--err">820ms</div>
              </div>
              <div class="tw-span-row tw-span-row--error" data-span-idx="3" onclick="selectSpanV2(this, 3)">
                <div class="tw-cell-name" style="padding-left:48px">
                  <span class="tw-toggle-spacer"></span>
                  <span class="tw-dot tw-dot--err"></span>
                  <span class="tw-svc-tag tw-svc--orange">inventory-svc</span>
                  <span class="tw-op-name">db.query</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar tw-bar--err" style="left:5%;width:63%;background:var(--accent-red);opacity:0.7"></div></div>
                <div class="tw-cell-dur tw-dur--err">780ms</div>
              </div>
              <div class="tw-span-row" data-span-idx="4" onclick="selectSpanV2(this, 4)">
                <div class="tw-cell-name" style="padding-left:48px">
                  <span class="tw-toggle-spacer"></span>
                  <span class="tw-dot tw-dot--ok"></span>
                  <span class="tw-svc-tag tw-svc--orange">inventory-svc</span>
                  <span class="tw-op-name">redis.get</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar" style="left:4%;width:1.5%;background:var(--accent-cyan)"></div></div>
                <div class="tw-cell-dur">18ms</div>
              </div>
              <div class="tw-span-row" data-span-idx="5" onclick="selectSpanV2(this, 5)">
                <div class="tw-cell-name" style="padding-left:24px">
                  <span class="tw-toggle-spacer"></span>
                  <span class="tw-dot tw-dot--ok"></span>
                  <span class="tw-svc-tag tw-svc--yellow">payment-gateway</span>
                  <span class="tw-op-name">charge</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar" style="left:72%;width:17%;background:var(--accent-yellow)"></div></div>
                <div class="tw-cell-dur">210ms</div>
              </div>
              <div class="tw-span-row" data-span-idx="6" onclick="selectSpanV2(this, 6)">
                <div class="tw-cell-name" style="padding-left:24px">
                  <span class="tw-toggle-spacer"></span>
                  <span class="tw-dot tw-dot--ok"></span>
                  <span class="tw-svc-tag tw-svc--purple">notification-svc</span>
                  <span class="tw-op-name">sendEmail</span>
                </div>
                <div class="tw-cell-timeline"><div class="tw-bar" style="left:90%;width:4%;background:var(--accent-purple)"></div></div>
                <div class="tw-cell-dur">45ms</div>
              </div>
            </div>
          </div>
          <!-- Right: Span Detail -->
          <div class="trace-drawer-span-detail">
            <div class="td-detail-title" id="td-detail-title">
              <div class="td-detail-name">
                <span id="td-span-name">POST /api/orders</span>
                <span class="td-detail-status td-detail-status--err" id="td-span-status">Error</span>
              </div>
              <div class="td-detail-meta">
                <span class="badge badge-info" id="td-span-svc">order-service</span>
                <span class="font-mono text-xs" style="color:var(--text-secondary)" id="td-span-dur">1,240ms</span>
                <span class="font-mono text-xs" style="color:var(--text-tertiary)" id="td-span-id">span_a1b2c3</span>
              </div>
            </div>
            <div class="td-detail-body" id="td-detail-body">
              <!-- Populated dynamically by TraceDrawer.selectSpan() -->
            </div>
          </div>
        </div>
      </div>
    `;
  },

  init: function(container) {
    // Initialize: auto-select first span detail when drawer is already showing
    var firstSpan = container.querySelector('.tw-span-row');
    if (firstSpan) {
      TraceDrawer.selectSpan(firstSpan, 0);
    }
  }
});

// Expose trace visualization tab switching globally
window.switchTraceViz = function(tabEl) {
  var parent = tabEl.parentElement;
  parent.querySelectorAll('.trace-viz-tab').forEach(function(t) { t.classList.remove('active'); });
  tabEl.classList.add('active');

  var vizId = tabEl.getAttribute('data-viz');
  var card = tabEl.closest('.card');
  card.querySelectorAll('.trace-viz-panel').forEach(function(p) { p.classList.remove('active'); });
  var panel = card.querySelector('#viz-' + vizId);
  if (panel) panel.classList.add('active');
};
