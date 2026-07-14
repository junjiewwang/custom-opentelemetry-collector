/**
 * Charts — 图表生成工具组件
 */
var Charts = (function() {
  'use strict';

  /**
   * 生成柱状图
   * @param {string} containerId - 容器元素ID
   * @param {number} count - 柱子数量
   * @param {number} minH - 最小高度百分比
   * @param {number} maxH - 最大高度百分比
   * @param {string} [color] - 可选颜色
   */
  function generateBars(containerId, count, minH, maxH, color) {
    var container = document.getElementById(containerId);
    if (!container) return;
    container.innerHTML = '';
    for (var i = 0; i < count; i++) {
      var bar = document.createElement('div');
      bar.className = 'chart-bar';
      var h = minH + Math.random() * (maxH - minH);
      bar.style.height = h + '%';
      if (color) bar.style.background = 'linear-gradient(180deg, ' + color + ' 0%, ' + color + '88 100%)';
      container.appendChild(bar);
    }
  }

  /**
   * 初始化 Dashboard 页面的图表
   */
  function initDashboardCharts() {
    generateBars('throughput-chart', 60, 30, 85);
    generateBars('latency-chart', 60, 15, 70, '#d29922');
  }

  /**
   * 初始化 Metrics 页面的图表
   */
  function initMetricsCharts() {
    generateBars('metrics-rate-chart', 60, 35, 90);
    generateBars('metrics-errors-chart', 60, 5, 40, '#f85149');
    generateBars('metrics-duration-chart', 60, 20, 75, '#bc8cff');
    generateBars('metrics-saturation-chart', 60, 25, 65, '#3fb950');
  }

  /**
   * 初始化 Resource Usage 页面的图表
   */
  function initResourceUsageCharts() {
    generateBars('storage-trend-chart', 60, 30, 75, '#bc8cff');
  }

  return {
    generateBars: generateBars,
    initDashboardCharts: initDashboardCharts,
    initMetricsCharts: initMetricsCharts,
    initResourceUsageCharts: initResourceUsageCharts
  };
})();
