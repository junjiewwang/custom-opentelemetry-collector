/**
 * scope.js — Scope Bar & Role Switcher
 * 
 * Responsibilities:
 * - Scope dropdown open/close/select
 * - Role toggle (admin ↔ tenant)
 * - Update UI visibility based on role
 */

let currentRole = 'admin'; // 'admin' | 'tenant'

/**
 * Initialize scope bar interactions
 */
export function initScope() {
  // Close dropdowns when clicking outside
  document.addEventListener('click', (e) => {
    if (!e.target.closest('.scope-picker')) {
      document.querySelectorAll('.scope-dropdown').forEach(d => d.classList.remove('scope-dropdown--open'));
    }
  });

  // Dropdown item selection
  document.querySelectorAll('.scope-dropdown-item').forEach(item => {
    item.addEventListener('click', (e) => {
      e.stopPropagation();
      const dropdown = item.closest('.scope-dropdown');
      const picker = item.closest('.scope-picker');
      const valueEl = picker.querySelector('.scope-picker-value');

      // Update active state
      dropdown.querySelectorAll('.scope-dropdown-item').forEach(i => i.classList.remove('scope-dropdown-item--active'));
      item.classList.add('scope-dropdown-item--active');

      // Update display value
      let text = item.textContent.trim().replace(/^✓\s*/, '');
      const meta = item.querySelector('.scope-dropdown-meta');
      if (meta) text = text.replace(meta.textContent, '').trim();
      valueEl.textContent = text;
      valueEl.style.color = item.classList.contains('scope-dropdown-item--all') ? 'var(--text-tertiary)' : '';

      dropdown.classList.remove('scope-dropdown--open');
    });
  });

  // Tab switching (generic)
  document.querySelectorAll('.tabs').forEach(tabGroup => {
    tabGroup.querySelectorAll('.tab').forEach(tab => {
      tab.addEventListener('click', () => {
        tabGroup.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
        tab.classList.add('active');
      });
    });
  });
}

/**
 * Toggle scope dropdown open/close
 */
export function toggleScopeDropdown(pickerId) {
  const dropdown = document.getElementById('dropdown-' + pickerId);
  if (!dropdown) return;

  // Close all other dropdowns
  document.querySelectorAll('.scope-dropdown').forEach(d => {
    if (d !== dropdown) d.classList.remove('scope-dropdown--open');
  });

  dropdown.classList.toggle('scope-dropdown--open');

  // Focus search input
  const searchInput = dropdown.querySelector('.scope-dropdown-input');
  if (searchInput && dropdown.classList.contains('scope-dropdown--open')) {
    setTimeout(() => searchInput.focus(), 50);
  }
}

/**
 * Toggle role between admin and tenant
 */
export function toggleRole() {
  currentRole = currentRole === 'admin' ? 'tenant' : 'admin';
  applyRole(currentRole);
}

/**
 * Apply role-based UI changes
 */
function applyRole(role) {
  const isAdmin = role === 'admin';
  const isTenant = role === 'tenant';

  // 1. Toggle slider visual
  const slider = document.getElementById('role-slider');
  const options = document.querySelectorAll('.role-option');
  options.forEach(o => o.classList.remove('role-option--active'));
  document.querySelector(`.role-option[data-role="${role}"]`).classList.add('role-option--active');
  slider.style.transform = isTenant ? 'translateX(100%)' : 'translateX(0)';

  // 2. Scope Bar: hide/show Tenant picker + its separator
  const tenantPicker = document.getElementById('scope-tenant');
  const tenantSep = tenantPicker.nextElementSibling;
  tenantPicker.style.display = isTenant ? 'none' : '';
  tenantSep.style.display = isTenant ? 'none' : '';

  // 3. Sidebar Header toggle
  document.getElementById('sidebar-header-admin').style.display = isAdmin ? '' : 'none';
  document.getElementById('sidebar-header-tenant').style.display = isTenant ? '' : 'none';

  // 4. Nav groups: toggle admin-only / tenant-only visibility
  document.querySelectorAll('[data-admin-only]').forEach(el => {
    el.style.display = isTenant ? 'none' : '';
  });
  document.querySelectorAll('[data-tenant-only]').forEach(el => {
    el.style.display = isTenant ? '' : 'none';
  });

  // 5. Bottom nav: settings vs user profile
  document.getElementById('nav-settings-admin').style.display = isAdmin ? '' : 'none';
  document.getElementById('nav-settings-tenant').style.display = isTenant ? '' : 'none';

  // 6. Scope context tag update
  const ctxTag = document.getElementById('scope-context-tag');
  if (isTenant) {
    ctxTag.innerHTML = '<i class="fas fa-building" style="font-size:0.55rem"></i> Acme Corp / order-service';
  } else {
    ctxTag.innerHTML = '<i class="fas fa-filter" style="font-size:0.55rem"></i> Acme Corp / E-Commerce Platform / order-service';
  }

  // 7. If tenant was viewing admin-only page, redirect to dashboard
  const adminOnlyPages = ['page-platform-dashboard', 'page-tenants', 'page-resource-usage', 'page-global-errors', 'page-global-alerts', 'page-resources'];
  const activePage = document.querySelector('.page.active');
  if (isTenant && activePage && adminOnlyPages.includes(activePage.id)) {
    window.navigateToPage('dashboard');
  }

  // 8. If switching to admin, go to Platform Dashboard
  if (isAdmin) {
    window.navigateToPage('platform-dashboard');
  }
}

/**
 * Switch to tenant view (from admin tenant table)
 */
export function switchToTenantView(tenantId) {
  currentRole = 'tenant';
  applyRole('tenant');
}

// Expose globally for inline onclick handlers
window.toggleScopeDropdown = toggleScopeDropdown;
window.toggleRole = toggleRole;
window.switchToTenantView = switchToTenantView;
