/**
 * TraceDrawer — Trace 详情抽屉组件
 * 包含打开/关闭、Span 选择、属性展示等逻辑
 */
var TraceDrawer = (function() {
  'use strict';

  function open(rowEl, traceId) {
    // Update selected state in table
    document.querySelectorAll('.trace-row').forEach(function(r) { r.classList.remove('trace-row--selected'); });
    rowEl.classList.add('trace-row--selected');

    // Update drawer top bar info
    document.getElementById('drawer-trace-id-v2').textContent = traceId;

    // Show overlay and drawer
    var overlay = document.getElementById('trace-drawer-overlay');
    var drawer = document.getElementById('trace-drawer');
    overlay.classList.add('trace-drawer-overlay--open');
    drawer.offsetHeight; // trigger reflow
    drawer.classList.add('trace-drawer--open');

    // Auto-select first span in waterfall
    var firstSpan = drawer.querySelector('.tw-span-row');
    if (firstSpan) {
      selectSpan(firstSpan, 0);
    }
  }

  function close() {
    var overlay = document.getElementById('trace-drawer-overlay');
    var drawer = document.getElementById('trace-drawer');
    drawer.classList.remove('trace-drawer--open');
    overlay.classList.remove('trace-drawer-overlay--open');
  }

  function selectSpan(spanEl, spanIndex) {
    // Update active state in waterfall
    var body = spanEl.closest('.tw-tree-body') || spanEl.parentElement;
    body.querySelectorAll('.tw-span-row').forEach(function(r) { r.classList.remove('tw-span-row--selected'); });
    spanEl.classList.add('tw-span-row--selected');

    var data = MockData.spanData[spanIndex] || MockData.spanData[0];

    // Update detail title bar
    document.getElementById('td-span-name').textContent = data.name;
    document.getElementById('td-span-svc').textContent = data.service;
    document.getElementById('td-span-dur').textContent = data.duration;
    document.getElementById('td-span-id').textContent = data.spanId;

    var statusEl = document.getElementById('td-span-status');
    statusEl.textContent = data.status;
    statusEl.className = 'td-detail-status ' + (data.status === 'Error' ? 'td-detail-status--err' : 'td-detail-status--ok');

    // Build detail body HTML
    var html = '';

    // Attributes
    html += '<div class="span-attr-section"><div class="span-attr-section-title"><i class="fas fa-tags"></i> Attributes</div><div class="span-attr-table">';
    for (var key in data.attrs) {
      if (!data.attrs.hasOwnProperty(key)) continue;
      var val = data.attrs[key];
      var isLong = String(val).length > 60;
      var cls = isLong ? ' span-attr-value--truncated' : '';
      var onclick = isLong ? ' onclick="TraceDrawer.toggleAttrExpand(this)"' : '';
      html += '<div class="span-attr-row"><span class="span-attr-key">' + key + '</span><span class="span-attr-value' + cls + '"' + onclick + '>' + val + '</span></div>';
    }
    html += '</div></div>';

    // Resource
    html += '<div class="span-attr-section"><div class="span-attr-section-title"><i class="fas fa-server"></i> Resource</div><div class="span-attr-table">';
    for (var rkey in data.resource) {
      if (!data.resource.hasOwnProperty(rkey)) continue;
      html += '<div class="span-attr-row"><span class="span-attr-key">' + rkey + '</span><span class="span-attr-value">' + data.resource[rkey] + '</span></div>';
    }
    html += '</div></div>';

    // Exception
    if (data.exception) {
      html += '<div class="span-attr-section"><div class="span-attr-section-title" style="color:var(--accent-red)"><i class="fas fa-exclamation-circle"></i> Exception</div><div class="span-attr-table">';
      html += '<div class="span-attr-row"><span class="span-attr-key">exception.type</span><span class="span-attr-value" style="color:var(--accent-red)">' + data.exception.type + '</span></div>';
      html += '<div class="span-attr-row"><span class="span-attr-key">exception.message</span><span class="span-attr-value span-attr-value--truncated" onclick="TraceDrawer.toggleAttrExpand(this)">' + data.exception.message + '</span></div>';
      html += '<div class="span-attr-row"><span class="span-attr-key">exception.stacktrace</span><span class="span-attr-value span-attr-value--truncated" onclick="TraceDrawer.toggleAttrExpand(this)">' + data.exception.stacktrace + '</span></div>';
      html += '</div></div>';
    }

    // Events
    html += '<div class="span-attr-section"><div class="span-attr-section-title"><i class="fas fa-bolt"></i> Events</div><div class="span-attr-table">';
    for (var i = 0; i < data.events.length; i++) {
      var evt = data.events[i];
      var evtColor = evt.msg.toLowerCase().indexOf('fail') !== -1 || evt.msg.toLowerCase().indexOf('timeout') !== -1 || evt.msg.toLowerCase().indexOf('error') !== -1 || evt.msg.toLowerCase().indexOf('exception') !== -1 ? 'color:var(--accent-red)' : '';
      html += '<div class="span-attr-row"><span class="span-attr-key" style="color:var(--text-tertiary)">' + evt.time + '</span><span class="span-attr-value" style="' + evtColor + '">' + evt.msg + '</span></div>';
    }
    html += '</div></div>';

    document.getElementById('td-detail-body').innerHTML = html;
  }

  function toggleAttrExpand(el) {
    if (el.classList.contains('span-attr-value--expanded')) {
      el.classList.remove('span-attr-value--expanded');
      el.classList.add('span-attr-value--truncated');
    } else {
      el.classList.remove('span-attr-value--truncated');
      el.classList.add('span-attr-value--expanded');
    }
  }

  // Expose globally for inline onclick handlers
  window.openTraceDrawer = open;
  window.closeTraceDrawer = close;
  window.selectSpanV2 = selectSpan;
  window.toggleAttrExpand = toggleAttrExpand;

  return {
    open: open,
    close: close,
    selectSpan: selectSpan,
    toggleAttrExpand: toggleAttrExpand
  };
})();
