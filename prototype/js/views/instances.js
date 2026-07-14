/**
 * Instances Page View
 * Shows all service instances with status, CPU/Memory, GC, Uptime, Agent version
 */
ViewRouter.register('instances', {
  render: function(container) {
    container.innerHTML = `
      <div class="filter-bar">
        <i class="fas fa-search" style="color:var(--text-tertiary)"></i>
        <input class="filter-input" placeholder="Search instances by hostname, IP, service...">
        <div class="flex gap-sm">
          <button class="btn btn-ghost" style="color:var(--accent-green)"><span class="dot dot-healthy"></span> 14 Online</button>
          <button class="btn btn-ghost" style="color:var(--accent-red)"><span class="dot dot-critical"></span> 2 Offline</button>
        </div>
      </div>

      <div class="card">
        <div class="table-container">
          <table>
            <thead><tr><th>Instance</th><th>Service</th><th>Status</th><th>CPU</th><th>Memory</th><th>GC Pause</th><th>Uptime</th><th>Agent</th></tr></thead>
            <tbody>
              <tr>
                <td><span class="font-mono">order-svc-pod-7f4d8</span><br><span class="text-xs text-tertiary">10.0.2.15</span></td>
                <td>order-service</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Online</span></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:35%;height:100%;background:var(--accent-green);border-radius:3px"></div></div><span class="text-xs font-mono">35%</span></div></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:62%;height:100%;background:var(--accent-yellow);border-radius:3px"></div></div><span class="text-xs font-mono">62%</span></div></td>
                <td class="font-mono">12ms</td>
                <td class="text-secondary">3d 14h</td>
                <td><span class="badge badge-info">v1.4.2</span></td>
              </tr>
              <tr>
                <td><span class="font-mono">order-svc-pod-a2b3c</span><br><span class="text-xs text-tertiary">10.0.2.16</span></td>
                <td>order-service</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Online</span></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:42%;height:100%;background:var(--accent-green);border-radius:3px"></div></div><span class="text-xs font-mono">42%</span></div></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:58%;height:100%;background:var(--accent-yellow);border-radius:3px"></div></div><span class="text-xs font-mono">58%</span></div></td>
                <td class="font-mono">15ms</td>
                <td class="text-secondary">3d 14h</td>
                <td><span class="badge badge-info">v1.4.2</span></td>
              </tr>
              <tr>
                <td><span class="font-mono">payment-gw-pod-x9y8z</span><br><span class="text-xs text-tertiary">10.0.3.21</span></td>
                <td>payment-gateway</td>
                <td><span class="badge badge-warning"><span class="dot dot-warning"></span>High Load</span></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:87%;height:100%;background:var(--accent-red);border-radius:3px"></div></div><span class="text-xs font-mono" style="color:var(--accent-red)">87%</span></div></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:78%;height:100%;background:var(--accent-orange);border-radius:3px"></div></div><span class="text-xs font-mono" style="color:var(--accent-orange)">78%</span></div></td>
                <td class="font-mono" style="color:var(--accent-yellow)">45ms</td>
                <td class="text-secondary">1d 6h</td>
                <td><span class="badge badge-info">v1.4.1</span></td>
              </tr>
              <tr>
                <td><span class="font-mono">inv-svc-pod-m1n2o</span><br><span class="text-xs text-tertiary">10.0.4.8</span></td>
                <td>inventory-svc</td>
                <td><span class="badge badge-critical"><span class="dot dot-critical"></span>Offline</span></td>
                <td><span class="text-xs text-tertiary">\u2014</span></td>
                <td><span class="text-xs text-tertiary">\u2014</span></td>
                <td><span class="text-xs text-tertiary">\u2014</span></td>
                <td class="text-secondary text-xs">Last seen 12min ago</td>
                <td><span class="badge badge-neutral">v1.4.0</span></td>
              </tr>
              <tr>
                <td><span class="font-mono">user-auth-pod-k5l6m</span><br><span class="text-xs text-tertiary">10.0.1.5</span></td>
                <td>user-auth</td>
                <td><span class="badge badge-healthy"><span class="dot dot-healthy"></span>Online</span></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:22%;height:100%;background:var(--accent-green);border-radius:3px"></div></div><span class="text-xs font-mono">22%</span></div></td>
                <td><div class="flex items-center gap-sm"><div style="width:60px;height:6px;background:var(--bg-tertiary);border-radius:3px;overflow:hidden"><div style="width:45%;height:100%;background:var(--accent-green);border-radius:3px"></div></div><span class="text-xs font-mono">45%</span></div></td>
                <td class="font-mono">8ms</td>
                <td class="text-secondary">7d 2h</td>
                <td><span class="badge badge-info">v1.4.2</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    `;
  }
});
