/**
 * App Entry Point
 * Initializes ViewRouter, binds sidebar navigation, sets default view
 */
(function() {
  'use strict';

  // ---- Navigation: sidebar nav items ----
  function bindNavigation() {
    document.querySelectorAll('.nav-item[data-page]').forEach(function(item) {
      item.addEventListener('click', function() {
        var pageId = item.dataset.page;
        navigateToPage(pageId);
      });
    });
  }

  // ---- Navigate to page (used by nav clicks and role switching) ----
  window.navigateToPage = function(pageId) {
    // Update nav active state
    document.querySelectorAll('.nav-item').forEach(function(n) { n.classList.remove('active'); });
    var navItem = document.querySelector('.nav-item[data-page="' + pageId + '"]');
    if (navItem) navItem.classList.add('active');

    // Update breadcrumb title — get text from the nav item (excluding badge text)
    var titleEl = document.getElementById('page-title');
    if (titleEl && navItem) {
      var textContent = '';
      navItem.childNodes.forEach(function(node) {
        if (node.nodeType === 3) { // Text node
          textContent += node.textContent;
        }
      });
      textContent = textContent.trim();
      // Fallback: use full textContent minus badge
      if (!textContent) {
        textContent = navItem.textContent.trim();
        var badge = navItem.querySelector('.nav-badge');
        if (badge) textContent = textContent.replace(badge.textContent, '').trim();
      }
      titleEl.textContent = textContent;
    }

    // Route to view
    ViewRouter.navigate(pageId);
  };

  // ---- Switch to Tenant View (from admin tenant table) ----
  window.switchToTenantView = function(tenantId) {
    ScopeBar.applyRole('tenant');
  };

  // ---- Tab switching (generic, for statically rendered tabs) ----
  function bindTabSwitching() {
    // Use event delegation on the page container for dynamically rendered tabs
    var pageContainer = document.getElementById('page-container');
    if (pageContainer) {
      pageContainer.addEventListener('click', function(e) {
        var tab = e.target.closest('.tab');
        if (!tab) return;
        var tabGroup = tab.closest('.tabs');
        if (!tabGroup) return;
        tabGroup.querySelectorAll('.tab').forEach(function(t) { t.classList.remove('active'); });
        tab.classList.add('active');
      });
    }
  }

  // ---- Keyboard shortcuts ----
  document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape') {
      // Close trace drawer first if open
      var drawer = document.getElementById('trace-drawer');
      if (drawer && drawer.classList.contains('trace-drawer--open')) {
        TraceDrawer.close();
        return;
      }
      // Close modal if open
      var modal = document.getElementById('new-rule-modal');
      if (modal && modal.classList.contains('modal-overlay--open')) {
        window.closeNewRuleModal && window.closeNewRuleModal();
      }
    }
  });

  // ---- Initialize ----
  function init() {
    bindNavigation();
    bindTabSwitching();
    ScopeBar.init();

    // Apply initial role state to sync DOM
    var initialRole = ScopeBar.getCurrentRole();
    ScopeBar.applyRole(initialRole);
  }

  // Run on DOM ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
