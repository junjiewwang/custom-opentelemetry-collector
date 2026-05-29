/**
 * View: Retention Settings (Platform Admin)
 * 管理全局默认策略 + 平台上限 + 查看所有租户的 App 策略
 */
ViewRouter.register('retention-settings', {
  render: function(container) {
    container.innerHTML = '\
      <!-- Platform Limits & Defaults -->\
      <div class="stat-grid mb-xl">\
        <div class="stat-card">\
          <div class="stat-card-icon blue"><i class="fas fa-shield-alt"></i></div>\
          <div class="stat-card-label">Platform Max TTL</div>\
          <div class="stat-card-value" style="font-size:1.2rem">Trace 30d / Metric 90d / Log 30d</div>\
          <div class="stat-card-trend neutral"><i class="fas fa-lock"></i> Global upper bound</div>\
        </div>\
        <div class="stat-card">\
          <div class="stat-card-icon green"><i class="fas fa-layer-group"></i></div>\
          <div class="stat-card-label">Default Policy</div>\
          <div class="stat-card-value" style="font-size:1.2rem">Trace 7d / Metric 30d / Log 14d</div>\
          <div class="stat-card-trend neutral"><i class="fas fa-info-circle"></i> Applied to new Apps</div>\
        </div>\
        <div class="stat-card">\
          <div class="stat-card-icon purple"><i class="fas fa-users-cog"></i></div>\
          <div class="stat-card-label">Custom Policies</div>\
          <div class="stat-card-value">5</div>\
          <div class="stat-card-trend neutral"><i class="fas fa-building"></i> Across 3 tenants</div>\
        </div>\
        <div class="stat-card">\
          <div class="stat-card-icon yellow"><i class="fas fa-broom"></i></div>\
          <div class="stat-card-label">Last Cleanup</div>\
          <div class="stat-card-value" style="font-size:1.2rem">2h ago</div>\
          <div class="stat-card-trend up"><i class="fas fa-check-circle"></i> 12.4K spans removed</div>\
        </div>\
      </div>\
      \
      <!-- Platform Settings Card -->\
      <div class="card mb-xl">\
        <div class="card-header">\
          <span class="card-title"><i class="fas fa-cog"></i> Platform Retention Settings</span>\
          <button class="btn btn-primary" id="btn-save-platform-settings"><i class="fas fa-save"></i> Save Changes</button>\
        </div>\
        <div class="card-body">\
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:24px">\
            <!-- Default Policy -->\
            <div>\
              <h3 style="font-size:0.85rem;color:var(--text-secondary);margin-bottom:12px;text-transform:uppercase;letter-spacing:0.5px">Default Policy (Fallback)</h3>\
              <div style="display:flex;flex-direction:column;gap:12px">\
                <div class="retention-input-row">\
                  <label class="retention-label"><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</label>\
                  <div class="retention-input-group"><input type="number" class="retention-input" value="7" min="1" max="30"><span class="retention-unit">days</span></div>\
                </div>\
                <div class="retention-input-row">\
                  <label class="retention-label"><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</label>\
                  <div class="retention-input-group"><input type="number" class="retention-input" value="30" min="1" max="90"><span class="retention-unit">days</span></div>\
                </div>\
                <div class="retention-input-row">\
                  <label class="retention-label"><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</label>\
                  <div class="retention-input-group"><input type="number" class="retention-input" value="14" min="1" max="30"><span class="retention-unit">days</span></div>\
                </div>\
              </div>\
            </div>\
            <!-- Max TTL -->\
            <div>\
              <h3 style="font-size:0.85rem;color:var(--text-secondary);margin-bottom:12px;text-transform:uppercase;letter-spacing:0.5px">Platform Max TTL (Upper Bound)</h3>\
              <div style="display:flex;flex-direction:column;gap:12px">\
                <div class="retention-input-row">\
                  <label class="retention-label"><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</label>\
                  <div class="retention-input-group"><input type="number" class="retention-input" value="30" min="1" max="365"><span class="retention-unit">days max</span></div>\
                </div>\
                <div class="retention-input-row">\
                  <label class="retention-label"><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</label>\
                  <div class="retention-input-group"><input type="number" class="retention-input" value="90" min="1" max="365"><span class="retention-unit">days max</span></div>\
                </div>\
                <div class="retention-input-row">\
                  <label class="retention-label"><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</label>\
                  <div class="retention-input-group"><input type="number" class="retention-input" value="30" min="1" max="365"><span class="retention-unit">days max</span></div>\
                </div>\
              </div>\
            </div>\
          </div>\
        </div>\
      </div>\
      \
      <!-- All Tenant Policies -->\
      <div class="card mb-xl">\
        <div class="card-header">\
          <span class="card-title"><i class="fas fa-building"></i> Tenant App Retention Policies</span>\
          <span class="text-xs text-tertiary">All tenants · All apps</span>\
        </div>\
        <div class="card-body" style="padding:0">\
          <div class="table-container"><table>\
            <thead><tr><th>Tenant</th><th>App</th><th>Traces</th><th>Metrics</th><th>Logs</th><th>Storage Used</th><th>Policy Type</th><th>Last Cleanup</th></tr></thead>\
            <tbody>\
              <tr>\
                <td><i class="fas fa-building" style="color:var(--accent-blue);margin-right:6px"></i><strong>Acme Corp</strong></td>\
                <td><span class="badge badge-info">E-Commerce Platform</span></td>\
                <td class="font-mono">14d</td>\
                <td class="font-mono">60d</td>\
                <td class="font-mono">14d</td>\
                <td class="font-mono">2.3 TB</td>\
                <td><span class="badge badge-warning">Custom</span></td>\
                <td class="text-secondary">2h ago</td>\
              </tr>\
              <tr>\
                <td><i class="fas fa-building" style="color:var(--accent-blue);margin-right:6px"></i><strong>Acme Corp</strong></td>\
                <td><span class="badge badge-info">Internal Tools</span></td>\
                <td class="font-mono" style="color:var(--text-tertiary)">7d</td>\
                <td class="font-mono" style="color:var(--text-tertiary)">30d</td>\
                <td class="font-mono" style="color:var(--text-tertiary)">14d</td>\
                <td class="font-mono">450 GB</td>\
                <td><span class="badge badge-neutral">Default</span></td>\
                <td class="text-secondary">2h ago</td>\
              </tr>\
              <tr>\
                <td><i class="fas fa-building" style="color:var(--accent-blue);margin-right:6px"></i><strong>Acme Corp</strong></td>\
                <td><span class="badge badge-info">Data Pipeline</span></td>\
                <td class="font-mono">3d</td>\
                <td class="font-mono">7d</td>\
                <td class="font-mono">3d</td>\
                <td class="font-mono">180 GB</td>\
                <td><span class="badge badge-warning">Custom</span></td>\
                <td class="text-secondary">2h ago</td>\
              </tr>\
              <tr>\
                <td><i class="fas fa-building" style="color:var(--accent-green);margin-right:6px"></i><strong>Beta Inc</strong></td>\
                <td><span class="badge badge-info">Analytics Platform</span></td>\
                <td class="font-mono">30d</td>\
                <td class="font-mono">90d</td>\
                <td class="font-mono">30d</td>\
                <td class="font-mono">1.8 TB</td>\
                <td><span class="badge badge-warning">Custom</span></td>\
                <td class="text-secondary">2h ago</td>\
              </tr>\
              <tr>\
                <td><i class="fas fa-building" style="color:var(--accent-purple);margin-right:6px"></i><strong>Gamma Ltd</strong></td>\
                <td><span class="badge badge-info">Payment Gateway</span></td>\
                <td class="font-mono">21d</td>\
                <td class="font-mono">60d</td>\
                <td class="font-mono">21d</td>\
                <td class="font-mono">920 GB</td>\
                <td><span class="badge badge-warning">Custom</span></td>\
                <td class="text-secondary">2h ago</td>\
              </tr>\
              <tr>\
                <td><i class="fas fa-building" style="color:var(--accent-purple);margin-right:6px"></i><strong>Gamma Ltd</strong></td>\
                <td><span class="badge badge-info">Notification Service</span></td>\
                <td class="font-mono" style="color:var(--text-tertiary)">7d</td>\
                <td class="font-mono" style="color:var(--text-tertiary)">30d</td>\
                <td class="font-mono" style="color:var(--text-tertiary)">14d</td>\
                <td class="font-mono">210 GB</td>\
                <td><span class="badge badge-neutral">Default</span></td>\
                <td class="text-secondary">2h ago</td>\
              </tr>\
            </tbody>\
          </table></div>\
        </div>\
      </div>\
      \
      <!-- Cleanup Schedule -->\
      <div class="card">\
        <div class="card-header">\
          <span class="card-title"><i class="fas fa-clock"></i> Cleanup Schedule & History</span>\
          <div style="display:flex;gap:8px">\
            <button class="btn btn-ghost" id="btn-dry-run"><i class="fas fa-eye"></i> Dry Run</button>\
            <button class="btn btn-primary" id="btn-run-cleanup"><i class="fas fa-play"></i> Run Cleanup Now</button>\
          </div>\
        </div>\
        <div class="card-body">\
          <div style="display:grid;grid-template-columns:1fr 2fr;gap:24px">\
            <div>\
              <h3 style="font-size:0.85rem;color:var(--text-secondary);margin-bottom:12px;text-transform:uppercase;letter-spacing:0.5px">Schedule</h3>\
              <div style="display:flex;flex-direction:column;gap:8px">\
                <div style="display:flex;justify-content:space-between;padding:8px 12px;background:var(--bg-tertiary);border-radius:6px">\
                  <span class="text-secondary">Frequency</span>\
                  <span class="font-mono">Daily at 02:00 UTC</span>\
                </div>\
                <div style="display:flex;justify-content:space-between;padding:8px 12px;background:var(--bg-tertiary);border-radius:6px">\
                  <span class="text-secondary">Batch Size</span>\
                  <span class="font-mono">10,000</span>\
                </div>\
                <div style="display:flex;justify-content:space-between;padding:8px 12px;background:var(--bg-tertiary);border-radius:6px">\
                  <span class="text-secondary">Status</span>\
                  <span style="color:var(--accent-green)"><i class="fas fa-check-circle"></i> Enabled</span>\
                </div>\
              </div>\
            </div>\
            <div>\
              <h3 style="font-size:0.85rem;color:var(--text-secondary);margin-bottom:12px;text-transform:uppercase;letter-spacing:0.5px">Recent Cleanup Reports</h3>\
              <div style="display:flex;flex-direction:column;gap:6px">\
                <div class="retention-report-row">\
                  <span class="retention-report-time">2026-05-29 02:00</span>\
                  <span class="badge badge-success">Success</span>\
                  <span class="font-mono">12,438 spans</span>\
                  <span class="font-mono">3,201 metrics</span>\
                  <span class="font-mono">8,912 logs</span>\
                  <span class="text-secondary">Duration: 4.2s</span>\
                </div>\
                <div class="retention-report-row">\
                  <span class="retention-report-time">2026-05-28 02:00</span>\
                  <span class="badge badge-success">Success</span>\
                  <span class="font-mono">11,892 spans</span>\
                  <span class="font-mono">2,980 metrics</span>\
                  <span class="font-mono">7,654 logs</span>\
                  <span class="text-secondary">Duration: 3.8s</span>\
                </div>\
                <div class="retention-report-row">\
                  <span class="retention-report-time">2026-05-27 02:00</span>\
                  <span class="badge badge-success">Success</span>\
                  <span class="font-mono">13,102 spans</span>\
                  <span class="font-mono">3,450 metrics</span>\
                  <span class="font-mono">9,231 logs</span>\
                  <span class="text-secondary">Duration: 4.6s</span>\
                </div>\
              </div>\
            </div>\
          </div>\
        </div>\
      </div>';
  },
  init: function() {
    // Save button interaction
    var saveBtn = document.getElementById('btn-save-platform-settings');
    if (saveBtn) {
      saveBtn.addEventListener('click', function() {
        saveBtn.innerHTML = '<i class="fas fa-check"></i> Saved!';
        saveBtn.style.background = 'var(--accent-green)';
        setTimeout(function() {
          saveBtn.innerHTML = '<i class="fas fa-save"></i> Save Changes';
          saveBtn.style.background = '';
        }, 2000);
      });
    }

    // Dry run button
    var dryRunBtn = document.getElementById('btn-dry-run');
    if (dryRunBtn) {
      dryRunBtn.addEventListener('click', function() {
        dryRunBtn.innerHTML = '<i class="fas fa-spinner fa-spin"></i> Analyzing...';
        setTimeout(function() {
          dryRunBtn.innerHTML = '<i class="fas fa-eye"></i> Dry Run';
          alert('Dry Run Preview:\n\n- Traces: 14,230 spans to delete\n- Metrics: 3,890 series to delete\n- Logs: 9,120 entries to delete\n\nNo data was actually removed.');
        }, 1000);
      });
    }

    // Run cleanup button
    var cleanupBtn = document.getElementById('btn-run-cleanup');
    if (cleanupBtn) {
      cleanupBtn.addEventListener('click', function() {
        if (confirm('Run cleanup now? This will delete expired data based on current policies.')) {
          cleanupBtn.innerHTML = '<i class="fas fa-spinner fa-spin"></i> Running...';
          setTimeout(function() {
            cleanupBtn.innerHTML = '<i class="fas fa-check"></i> Done!';
            cleanupBtn.style.background = 'var(--accent-green)';
            setTimeout(function() {
              cleanupBtn.innerHTML = '<i class="fas fa-play"></i> Run Cleanup Now';
              cleanupBtn.style.background = '';
            }, 2000);
          }, 2000);
        }
      });
    }
  }
});
