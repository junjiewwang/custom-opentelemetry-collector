/**
 * charts.js — Chart Generation Utilities
 * 
 * Responsibilities:
 * - Generate placeholder bar charts for dashboard/metrics pages
 * - Can be called after page HTML is loaded
 */

/**
 * Generate bars for chart placeholders
 */
export function generateBars(containerId, count, minH, maxH, color) {
  const container = document.getElementById(containerId);
  if (!container) return;
  container.innerHTML = '';
  for (let i = 0; i < count; i++) {
    const bar = document.createElement('div');
    bar.className = 'chart-bar';
    const h = minH + Math.random() * (maxH - minH);
    bar.style.height = h + '%';
    if (color) bar.style.background = `linear-gradient(180deg, ${color} 0%, ${color}88 100%)`;
    container.appendChild(bar);
  }
}

/**
 * Initialize all chart containers that exist in the DOM
 */
export function initCharts() {
  generateBars('throughput-chart', 60, 30, 85);
  generateBars('latency-chart', 60, 15, 70, '#d29922');
  generateBars('metrics-rate-chart', 60, 35, 90);
  generateBars('metrics-errors-chart', 60, 5, 40, '#f85149');
  generateBars('metrics-duration-chart', 60, 20, 75, '#bc8cff');
  generateBars('metrics-saturation-chart', 60, 25, 65, '#3fb950');
  generateBars('storage-trend-chart', 60, 30, 75, '#bc8cff');
}

// Expose globally for pages that need to regenerate charts after load
window.generateBars = generateBars;
window.initCharts = initCharts;
