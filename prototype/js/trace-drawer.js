/**
 * trace-drawer.js — Trace Drawer & Visualization
 * 
 * Responsibilities:
 * - Open/close trace detail drawer
 * - Select spans in waterfall view
 * - Render span detail (attributes, resource, exception, events)
 * - Switch trace visualization tabs (scatter/heatmap)
 * - Toggle attribute expand/collapse
 */

import { SPAN_DATA } from './mock-data.js';

/**
 * Initialize traces page interactions (called after page HTML is loaded)
 */
export function initTracesPage() {
  // Wire up trace drawer overlay click-to-close
  const overlay = document.getElementById('trace-drawer-overlay');
  if (overlay) {
    overlay.addEventListener('click', closeTraceDrawer);
  }
}

/**
 * Switch trace visualization tab (scatter/heatmap)
 */
export function switchTraceViz(tabEl) {
  const parent = tabEl.parentElement;
  parent.querySelectorAll('.trace-viz-tab').forEach(t => t.classList.remove('active'));
  tabEl.classList.add('active');

  const vizId = tabEl.getAttribute('data-viz');
  const card = tabEl.closest('.card');
  card.querySelectorAll('.trace-viz-panel').forEach(p => p.classList.remove('active'));
  const panel = card.querySelector('#viz-' + vizId);
  if (panel) panel.classList.add('active');
}

/**
 * Open the trace drawer for a given trace row
 */
export function openTraceDrawer(rowEl, traceId) {
  // Update selected state in table
  document.querySelectorAll('.trace-row').forEach(r => r.classList.remove('trace-row--selected'));
  rowEl.classList.add('trace-row--selected');

  // Update drawer top bar info
  document.getElementById('drawer-trace-id-v2').textContent = traceId;

  // Show overlay and drawer
  const overlay = document.getElementById('trace-drawer-overlay');
  const drawer = document.getElementById('trace-drawer');
  overlay.classList.add('trace-drawer-overlay--open');
  // Trigger reflow for animation
  drawer.offsetHeight;
  drawer.classList.add('trace-drawer--open');

  // Auto-select first span in waterfall
  const firstSpan = drawer.querySelector('.tw-span-row');
  if (firstSpan) {
    selectSpanV2(firstSpan, 0);
  }
}

/**
 * Close the trace drawer
 */
export function closeTraceDrawer() {
  const overlay = document.getElementById('trace-drawer-overlay');
  const drawer = document.getElementById('trace-drawer');
  if (drawer) drawer.classList.remove('trace-drawer--open');
  if (overlay) overlay.classList.remove('trace-drawer-overlay--open');
}

/**
 * Select a span in the waterfall grid and render its detail
 */
export function selectSpanV2(spanEl, spanIndex) {
  // Update active state in waterfall
  const body = spanEl.closest('.tw-tree-body') || spanEl.parentElement;
  body.querySelectorAll('.tw-span-row').forEach(r => r.classList.remove('tw-span-row--selected'));
  spanEl.classList.add('tw-span-row--selected');

  const data = SPAN_DATA[spanIndex] || SPAN_DATA[0];

  // Update detail title bar
  document.getElementById('td-span-name').textContent = data.name;
  document.getElementById('td-span-svc').textContent = data.service;
  document.getElementById('td-span-dur').textContent = data.duration;
  document.getElementById('td-span-id').textContent = data.spanId;

  const statusEl = document.getElementById('td-span-status');
  statusEl.textContent = data.status;
  statusEl.className = 'td-detail-status ' + (data.status === 'Error' ? 'td-detail-status--err' : 'td-detail-status--ok');

  // Build detail body HTML
  let html = '';

  // Attributes
  html += '<div class="span-attr-section"><div class="span-attr-section-title"><i class="fas fa-tags"></i> Attributes</div><div class="span-attr-table">';
  for (const [key, val] of Object.entries(data.attrs)) {
    const isLong = String(val).length > 60;
    const cls = isLong ? ' span-attr-value--truncated' : '';
    const onclick = isLong ? ' onclick="toggleAttrExpand(this)"' : '';
    html += `<div class="span-attr-row"><span class="span-attr-key">${key}</span><span class="span-attr-value${cls}"${onclick}>${val}</span></div>`;
  }
  html += '</div></div>';

  // Resource
  html += '<div class="span-attr-section"><div class="span-attr-section-title"><i class="fas fa-server"></i> Resource</div><div class="span-attr-table">';
  for (const [key, val] of Object.entries(data.resource)) {
    html += `<div class="span-attr-row"><span class="span-attr-key">${key}</span><span class="span-attr-value">${val}</span></div>`;
  }
  html += '</div></div>';

  // Exception (if any)
  if (data.exception) {
    html += '<div class="span-attr-section"><div class="span-attr-section-title" style="color:var(--accent-red)"><i class="fas fa-exclamation-circle"></i> Exception</div><div class="span-attr-table">';
    html += `<div class="span-attr-row"><span class="span-attr-key">exception.type</span><span class="span-attr-value" style="color:var(--accent-red)">${data.exception.type}</span></div>`;
    html += `<div class="span-attr-row"><span class="span-attr-key">exception.message</span><span class="span-attr-value span-attr-value--truncated" onclick="toggleAttrExpand(this)">${data.exception.message}</span></div>`;
    html += `<div class="span-attr-row"><span class="span-attr-key">exception.stacktrace</span><span class="span-attr-value span-attr-value--truncated" onclick="toggleAttrExpand(this)">${data.exception.stacktrace}</span></div>`;
    html += '</div></div>';
  }

  // Events
  html += '<div class="span-attr-section"><div class="span-attr-section-title"><i class="fas fa-bolt"></i> Events</div><div class="span-attr-table">';
  for (const evt of data.events) {
    const evtColor = evt.msg.toLowerCase().includes('fail') || evt.msg.toLowerCase().includes('timeout') || evt.msg.toLowerCase().includes('error') || evt.msg.toLowerCase().includes('exception') ? 'color:var(--accent-red)' : '';
    html += `<div class="span-attr-row"><span class="span-attr-key" style="color:var(--text-tertiary)">${evt.time}</span><span class="span-attr-value" style="${evtColor}">${evt.msg}</span></div>`;
  }
  html += '</div></div>';

  document.getElementById('td-detail-body').innerHTML = html;
}

/**
 * Toggle attribute value expand/collapse
 */
export function toggleAttrExpand(el) {
  if (el.classList.contains('span-attr-value--expanded')) {
    el.classList.remove('span-attr-value--expanded');
    el.classList.add('span-attr-value--truncated');
  } else {
    el.classList.remove('span-attr-value--truncated');
    el.classList.add('span-attr-value--expanded');
  }
}

// Expose globally for inline onclick handlers
window.switchTraceViz = switchTraceViz;
window.openTraceDrawer = openTraceDrawer;
window.closeTraceDrawer = closeTraceDrawer;
window.selectSpanV2 = selectSpanV2;
window.toggleAttrExpand = toggleAttrExpand;

// Register for keyboard handler
window.__apm_modules = window.__apm_modules || {};
window.__apm_modules['./trace-drawer.js'] = { closeTraceDrawer };
