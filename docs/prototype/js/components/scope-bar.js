/**
 * ScopeBar — Scope 切换组件逻辑
 * 包含 Role Switcher + Dropdown 管理
 */
var ScopeBar = (function() {
  'use strict';

  var currentRole = 'admin'; // 'admin' | 'tenant'

  function getCurrentRole() {
    return currentRole;
  }

  function toggleRole() {
    currentRole = currentRole === 'admin' ? 'tenant' : 'admin';
    applyRole(currentRole);
  }

  function applyRole(role) {
    currentRole = role;
    var isAdmin = role === 'admin';
    var isTenant = role === 'tenant';

    // 1. Toggle slider visual
    var slider = document.getElementById('role-slider');
    var options = document.querySelectorAll('.role-option');
    if (slider && options.length) {
      options.forEach(function(o) { o.classList.remove('role-option--active'); });
      var activeOpt = document.querySelector('.role-option[data-role="' + role + '"]');
      if (activeOpt) activeOpt.classList.add('role-option--active');
      slider.style.transform = isTenant ? 'translateX(100%)' : 'translateX(0)';
    }

    // 2. Scope Bar: hide/show Tenant picker + its separator (admin sees tenant picker to pick which tenant to drill into)
    var tenantPicker = document.getElementById('scope-tenant');
    if (tenantPicker) {
      tenantPicker.style.display = isTenant ? 'none' : '';
      var tenantSep = tenantPicker.nextElementSibling;
      if (tenantSep && tenantSep.classList.contains('scope-sep')) {
        tenantSep.style.display = isTenant ? 'none' : '';
      }
    }

    // 3. Sidebar Header toggle
    var headerAdmin = document.getElementById('sidebar-header-admin');
    var headerTenant = document.getElementById('sidebar-header-tenant');
    if (headerAdmin) headerAdmin.style.display = isAdmin ? '' : 'none';
    if (headerTenant) headerTenant.style.display = isTenant ? '' : 'none';

    // 4. Nav groups: toggle admin-only / tenant-only visibility
    document.querySelectorAll('[data-admin-only]').forEach(function(el) {
      el.style.display = isTenant ? 'none' : '';
    });
    document.querySelectorAll('[data-tenant-only]').forEach(function(el) {
      el.style.display = isTenant ? '' : 'none';
    });

    // 5. Bottom nav: settings vs user profile
    var navAdmin = document.getElementById('nav-settings-admin');
    var navTenant = document.getElementById('nav-settings-tenant');
    if (navAdmin) navAdmin.style.display = isAdmin ? '' : 'none';
    if (navTenant) navTenant.style.display = isTenant ? '' : 'none';

    // 6. Scope context tag update
    var ctxTag = document.getElementById('scope-context-tag');
    if (ctxTag) {
      if (isTenant) {
        ctxTag.innerHTML = '<i class="fas fa-building" style="font-size:0.55rem"></i> Acme Corp / order-service';
      } else {
        ctxTag.innerHTML = '<i class="fas fa-filter" style="font-size:0.55rem"></i> Acme Corp / E-Commerce Platform / order-service';
      }
    }

    // 7. If tenant was viewing admin-only page, redirect to tenant dashboard
    var adminOnlyPages = ['platform-dashboard', 'tenants', 'resource-usage', 'global-errors', 'global-alerts', 'resources'];
    var current = ViewRouter.getCurrentView();
    if (isTenant && adminOnlyPages.indexOf(current) !== -1) {
      if (window.navigateToPage) window.navigateToPage('dashboard');
    }

    // 8. If switching to admin, go to Platform Dashboard
    if (isAdmin && current !== 'platform-dashboard') {
      if (window.navigateToPage) window.navigateToPage('platform-dashboard');
    }
  }

  function toggleScopeDropdown(level, event) {
    // Prevent event from propagating to document click handler
    if (event) { event.stopPropagation(); }

    var dropdown = document.getElementById('dropdown-' + level);
    if (!dropdown) return;

    var allDropdowns = document.querySelectorAll('.scope-dropdown');
    allDropdowns.forEach(function(d) { if (d !== dropdown) d.classList.remove('scope-dropdown--open'); });
    dropdown.classList.toggle('scope-dropdown--open');
  }

  /**
   * 初始化 Scope Bar 事件
   */
  function init() {
    // Bind scope picker click events (replace inline onclick for better control)
    document.querySelectorAll('.scope-picker').forEach(function(picker) {
      picker.addEventListener('click', function(e) {
        // If clicking inside the dropdown, don't toggle
        if (e.target.closest('.scope-dropdown')) return;

        var pickerId = picker.id; // e.g. 'scope-tenant', 'scope-app', etc.
        var level = pickerId.replace('scope-', '');
        toggleScopeDropdown(level, e);
      });
    });

    // Close dropdowns on outside click
    document.addEventListener('click', function(e) {
      if (!e.target.closest('.scope-picker')) {
        document.querySelectorAll('.scope-dropdown').forEach(function(d) { d.classList.remove('scope-dropdown--open'); });
      }
    });

    // Scope dropdown item selection
    document.querySelectorAll('.scope-dropdown-item').forEach(function(item) {
      item.addEventListener('click', function(e) {
        e.stopPropagation();
        var dropdown = item.closest('.scope-dropdown');
        var picker = item.closest('.scope-picker');
        var valueEl = picker.querySelector('.scope-picker-value');

        // Update active state
        dropdown.querySelectorAll('.scope-dropdown-item').forEach(function(i) { i.classList.remove('scope-dropdown-item--active'); });
        item.classList.add('scope-dropdown-item--active');

        // Update display value
        var text = item.textContent.trim().replace(/^\u2713\s*/, '');
        var meta = item.querySelector('.scope-dropdown-meta');
        if (meta) text = text.replace(meta.textContent, '').trim();
        // Remove check icon text if present
        text = text.replace(/^\s*/, '').trim();
        valueEl.textContent = text;
        valueEl.style.color = item.classList.contains('scope-dropdown-item--all') ? 'var(--text-tertiary)' : '';

        dropdown.classList.remove('scope-dropdown--open');
      });
    });

    // Prevent clicks inside dropdown from triggering picker toggle
    document.querySelectorAll('.scope-dropdown').forEach(function(dropdown) {
      dropdown.addEventListener('click', function(e) {
        e.stopPropagation();
      });
    });

    // Role switcher
    var roleToggle = document.getElementById('role-toggle');
    if (roleToggle) {
      roleToggle.addEventListener('click', function(e) {
        e.stopPropagation();
        toggleRole();
      });
    }

    // Clear button: reset scope pickers to defaults
    var clearBtn = document.querySelector('.scope-clear-btn');
    if (clearBtn) {
      clearBtn.addEventListener('click', function() {
        // Reset all pickers to their first (or default) item
        var defaults = {
          'scope-tenant': 'Acme Corp',
          'scope-app': 'E-Commerce Platform',
          'scope-service': 'order-service',
          'scope-instance': 'All Instances'
        };
        for (var pickerId in defaults) {
          if (!defaults.hasOwnProperty(pickerId)) continue;
          var picker = document.getElementById(pickerId);
          if (!picker) continue;
          var valueEl = picker.querySelector('.scope-picker-value');
          if (valueEl) {
            valueEl.textContent = defaults[pickerId];
            valueEl.style.color = pickerId === 'scope-instance' ? 'var(--text-tertiary)' : '';
          }
          // Reset active state in dropdown
          var dropdown = picker.querySelector('.scope-dropdown');
          if (dropdown) {
            dropdown.querySelectorAll('.scope-dropdown-item').forEach(function(item, idx) {
              item.classList.toggle('scope-dropdown-item--active', idx === 0 || (pickerId === 'scope-app' && idx === 1) || (pickerId === 'scope-service' && idx === 0));
            });
          }
        }
      });
    }
  }

  // Expose globally for inline onclick handlers (backward compatibility)
  window.toggleRole = toggleRole;
  window.toggleScopeDropdown = toggleScopeDropdown;

  return {
    init: init,
    getCurrentRole: getCurrentRole,
    toggleRole: toggleRole,
    applyRole: applyRole,
    toggleScopeDropdown: toggleScopeDropdown
  };
})();
