/**
 * View: Data Retention (Tenant User)
 * 租户自主管理自己各 App 的保留策略
 */
ViewRouter.register('data-retention', {
  render: function(container) {
    container.innerHTML = '\
      <!-- Platform Limits Info Bar -->\
      <div class="retention-info-bar mb-xl">\
        <i class="fas fa-info-circle" style="color:var(--accent-blue)"></i>\
        <span>Platform limits: Traces max <strong>30 days</strong> · Metrics max <strong>90 days</strong> · Logs max <strong>30 days</strong></span>\
      </div>\
      \
      <!-- App Retention Cards -->\
      <div class="retention-app-list">\
        \
        <!-- App 1: Custom Policy -->\
        <div class="retention-app-card" id="retention-app-ecommerce">\
          <div class="retention-app-header">\
            <div class="retention-app-title">\
              <i class="fas fa-cube" style="color:var(--accent-blue)"></i>\
              <span>E-Commerce Platform</span>\
              <span class="badge badge-warning" style="font-size:0.65rem">Custom</span>\
            </div>\
            <div class="retention-app-meta">\
              <span class="text-secondary"><i class="fas fa-hdd"></i> 2.3 TB</span>\
              <span class="text-secondary"><i class="fas fa-clock"></i> Cleaned 2h ago</span>\
              <button class="btn btn-ghost retention-edit-btn" data-app="ecommerce"><i class="fas fa-pen"></i> Edit</button>\
            </div>\
          </div>\
          <div class="retention-app-signals">\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</span>\
                <span class="font-mono retention-signal-value">14 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:46.7%;background:var(--accent-blue)"></div></div>\
              <span class="retention-signal-limit">of 30d max</span>\
            </div>\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</span>\
                <span class="font-mono retention-signal-value">60 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:66.7%;background:var(--accent-green)"></div></div>\
              <span class="retention-signal-limit">of 90d max</span>\
            </div>\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</span>\
                <span class="font-mono retention-signal-value">14 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:46.7%;background:var(--accent-purple)"></div></div>\
              <span class="retention-signal-limit">of 30d max</span>\
            </div>\
          </div>\
          <!-- Edit Form (hidden by default) -->\
          <div class="retention-edit-form" id="edit-form-ecommerce" style="display:none">\
            <div class="retention-edit-grid">\
              <div class="retention-edit-field">\
                <label><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="14" min="1" max="30"><span class="retention-unit">days</span><span class="retention-max-hint">(max 30)</span></div>\
              </div>\
              <div class="retention-edit-field">\
                <label><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="60" min="1" max="90"><span class="retention-unit">days</span><span class="retention-max-hint">(max 90)</span></div>\
              </div>\
              <div class="retention-edit-field">\
                <label><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="14" min="1" max="30"><span class="retention-unit">days</span><span class="retention-max-hint">(max 30)</span></div>\
              </div>\
            </div>\
            <div class="retention-edit-actions">\
              <button class="btn btn-ghost retention-cancel-btn" data-app="ecommerce">Cancel</button>\
              <button class="btn btn-primary retention-save-btn" data-app="ecommerce"><i class="fas fa-check"></i> Save</button>\
            </div>\
          </div>\
        </div>\
        \
        <!-- App 2: Using Default -->\
        <div class="retention-app-card" id="retention-app-internal">\
          <div class="retention-app-header">\
            <div class="retention-app-title">\
              <i class="fas fa-cube" style="color:var(--accent-green)"></i>\
              <span>Internal Tools</span>\
              <span class="badge badge-neutral" style="font-size:0.65rem">Default</span>\
            </div>\
            <div class="retention-app-meta">\
              <span class="text-secondary"><i class="fas fa-hdd"></i> 450 GB</span>\
              <span class="text-secondary"><i class="fas fa-clock"></i> Cleaned 2h ago</span>\
              <button class="btn btn-ghost retention-edit-btn" data-app="internal"><i class="fas fa-pen"></i> Customize</button>\
            </div>\
          </div>\
          <div class="retention-app-signals">\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</span>\
                <span class="font-mono retention-signal-value" style="color:var(--text-tertiary)">7 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:23.3%;background:var(--text-tertiary)"></div></div>\
              <span class="retention-signal-limit">of 30d max (default)</span>\
            </div>\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</span>\
                <span class="font-mono retention-signal-value" style="color:var(--text-tertiary)">30 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:33.3%;background:var(--text-tertiary)"></div></div>\
              <span class="retention-signal-limit">of 90d max (default)</span>\
            </div>\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</span>\
                <span class="font-mono retention-signal-value" style="color:var(--text-tertiary)">14 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:46.7%;background:var(--text-tertiary)"></div></div>\
              <span class="retention-signal-limit">of 30d max (default)</span>\
            </div>\
          </div>\
          <!-- Edit Form -->\
          <div class="retention-edit-form" id="edit-form-internal" style="display:none">\
            <div class="retention-edit-grid">\
              <div class="retention-edit-field">\
                <label><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="7" min="1" max="30"><span class="retention-unit">days</span><span class="retention-max-hint">(max 30)</span></div>\
              </div>\
              <div class="retention-edit-field">\
                <label><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="30" min="1" max="90"><span class="retention-unit">days</span><span class="retention-max-hint">(max 90)</span></div>\
              </div>\
              <div class="retention-edit-field">\
                <label><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="14" min="1" max="30"><span class="retention-unit">days</span><span class="retention-max-hint">(max 30)</span></div>\
              </div>\
            </div>\
            <div class="retention-edit-actions">\
              <button class="btn btn-ghost retention-cancel-btn" data-app="internal">Cancel</button>\
              <button class="btn btn-primary retention-save-btn" data-app="internal"><i class="fas fa-check"></i> Save</button>\
            </div>\
          </div>\
        </div>\
        \
        <!-- App 3: Custom short retention -->\
        <div class="retention-app-card" id="retention-app-pipeline">\
          <div class="retention-app-header">\
            <div class="retention-app-title">\
              <i class="fas fa-cube" style="color:var(--accent-yellow)"></i>\
              <span>Data Pipeline</span>\
              <span class="badge badge-warning" style="font-size:0.65rem">Custom</span>\
            </div>\
            <div class="retention-app-meta">\
              <span class="text-secondary"><i class="fas fa-hdd"></i> 180 GB</span>\
              <span class="text-secondary"><i class="fas fa-clock"></i> Cleaned 2h ago</span>\
              <button class="btn btn-ghost retention-edit-btn" data-app="pipeline"><i class="fas fa-pen"></i> Edit</button>\
            </div>\
          </div>\
          <div class="retention-app-signals">\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</span>\
                <span class="font-mono retention-signal-value">3 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:10%;background:var(--accent-blue)"></div></div>\
              <span class="retention-signal-limit">of 30d max</span>\
            </div>\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</span>\
                <span class="font-mono retention-signal-value">7 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:7.8%;background:var(--accent-green)"></div></div>\
              <span class="retention-signal-limit">of 90d max</span>\
            </div>\
            <div class="retention-signal">\
              <div class="retention-signal-header">\
                <span class="retention-signal-icon"><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</span>\
                <span class="font-mono retention-signal-value">3 days</span>\
              </div>\
              <div class="retention-signal-bar"><div class="retention-signal-fill" style="width:10%;background:var(--accent-purple)"></div></div>\
              <span class="retention-signal-limit">of 30d max</span>\
            </div>\
          </div>\
          <!-- Edit Form -->\
          <div class="retention-edit-form" id="edit-form-pipeline" style="display:none">\
            <div class="retention-edit-grid">\
              <div class="retention-edit-field">\
                <label><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="3" min="1" max="30"><span class="retention-unit">days</span><span class="retention-max-hint">(max 30)</span></div>\
              </div>\
              <div class="retention-edit-field">\
                <label><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="7" min="1" max="90"><span class="retention-unit">days</span><span class="retention-max-hint">(max 90)</span></div>\
              </div>\
              <div class="retention-edit-field">\
                <label><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</label>\
                <div class="retention-input-group"><input type="number" class="retention-input" value="3" min="1" max="30"><span class="retention-unit">days</span><span class="retention-max-hint">(max 30)</span></div>\
              </div>\
            </div>\
            <div class="retention-edit-actions">\
              <button class="btn btn-ghost retention-cancel-btn" data-app="pipeline">Cancel</button>\
              <button class="btn btn-primary retention-save-btn" data-app="pipeline"><i class="fas fa-check"></i> Save</button>\
            </div>\
          </div>\
        </div>\
      </div>\
      \
      <!-- Cleanup Preview -->\
      <div class="card" style="margin-top:24px">\
        <div class="card-header">\
          <span class="card-title"><i class="fas fa-broom"></i> Cleanup Impact Preview</span>\
          <button class="btn btn-ghost" id="btn-tenant-dry-run"><i class="fas fa-eye"></i> Preview What Will Be Deleted</button>\
        </div>\
        <div class="card-body" id="tenant-dry-run-result" style="display:none">\
          <div class="table-container"><table>\
            <thead><tr><th>App</th><th>Signal</th><th>Records to Delete</th><th>Space to Reclaim</th><th>Data Older Than</th></tr></thead>\
            <tbody>\
              <tr><td>E-Commerce Platform</td><td><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</td><td class="font-mono">8,234</td><td class="font-mono">1.2 GB</td><td class="font-mono">14d</td></tr>\
              <tr><td>E-Commerce Platform</td><td><i class="fas fa-chart-area" style="color:var(--accent-green)"></i> Metrics</td><td class="font-mono">2,100</td><td class="font-mono">340 MB</td><td class="font-mono">60d</td></tr>\
              <tr><td>Data Pipeline</td><td><i class="fas fa-route" style="color:var(--accent-blue)"></i> Traces</td><td class="font-mono">15,892</td><td class="font-mono">890 MB</td><td class="font-mono">3d</td></tr>\
              <tr><td>Data Pipeline</td><td><i class="fas fa-file-alt" style="color:var(--accent-purple)"></i> Logs</td><td class="font-mono">42,310</td><td class="font-mono">2.1 GB</td><td class="font-mono">3d</td></tr>\
            </tbody>\
          </table></div>\
          <div style="margin-top:12px;padding:12px;background:var(--bg-tertiary);border-radius:6px;font-size:0.8rem;color:var(--text-secondary)">\
            <i class="fas fa-info-circle" style="color:var(--accent-blue)"></i>\
            This is a preview only. Actual cleanup runs daily at 02:00 UTC by the platform.\
          </div>\
        </div>\
      </div>';
  },
  init: function() {
    // Edit buttons
    document.querySelectorAll('.retention-edit-btn').forEach(function(btn) {
      btn.addEventListener('click', function() {
        var appId = btn.dataset.app;
        var form = document.getElementById('edit-form-' + appId);
        if (form) {
          form.style.display = form.style.display === 'none' ? '' : 'none';
        }
      });
    });

    // Cancel buttons
    document.querySelectorAll('.retention-cancel-btn').forEach(function(btn) {
      btn.addEventListener('click', function() {
        var appId = btn.dataset.app;
        var form = document.getElementById('edit-form-' + appId);
        if (form) form.style.display = 'none';
      });
    });

    // Save buttons
    document.querySelectorAll('.retention-save-btn').forEach(function(btn) {
      btn.addEventListener('click', function() {
        var appId = btn.dataset.app;
        // Validate max limits
        var form = document.getElementById('edit-form-' + appId);
        if (!form) return;
        var inputs = form.querySelectorAll('.retention-input');
        var valid = true;
        inputs.forEach(function(input) {
          var val = parseInt(input.value, 10);
          var max = parseInt(input.max, 10);
          if (val > max) {
            input.style.borderColor = 'var(--accent-red)';
            valid = false;
          } else {
            input.style.borderColor = '';
          }
        });
        if (!valid) {
          alert('Some values exceed platform limits. Please adjust.');
          return;
        }
        // Success feedback
        btn.innerHTML = '<i class="fas fa-check"></i> Saved!';
        btn.style.background = 'var(--accent-green)';
        setTimeout(function() {
          btn.innerHTML = '<i class="fas fa-check"></i> Save';
          btn.style.background = '';
          form.style.display = 'none';
        }, 1500);
      });
    });

    // Dry run button
    var dryRunBtn = document.getElementById('btn-tenant-dry-run');
    var dryRunResult = document.getElementById('tenant-dry-run-result');
    if (dryRunBtn && dryRunResult) {
      dryRunBtn.addEventListener('click', function() {
        dryRunBtn.innerHTML = '<i class="fas fa-spinner fa-spin"></i> Analyzing...';
        setTimeout(function() {
          dryRunBtn.innerHTML = '<i class="fas fa-eye"></i> Preview What Will Be Deleted';
          dryRunResult.style.display = '';
        }, 1000);
      });
    }
  }
});
