/**
 * resource-explorer.js — Resource Explorer Page Logic
 * 
 * Responsibilities:
 * - Tree node expand/collapse
 * - Node selection
 * - Search/filter tree nodes
 */

/**
 * Initialize Resource Explorer page interactions
 */
export function initResourceExplorer() {
  // Wire up node selection
  document.querySelectorAll('.re-node-row[data-select]').forEach(row => {
    row.addEventListener('click', (e) => {
      if (e.target.classList.contains('re-chevron')) return;
      document.querySelectorAll('.re-node-row--selected').forEach(r => r.classList.remove('re-node-row--selected'));
      row.classList.add('re-node-row--selected');
    });
  });

  // Wire up search filter
  const reSearchInput = document.getElementById('re-search-input');
  if (reSearchInput) {
    reSearchInput.addEventListener('input', (e) => {
      const query = e.target.value.toLowerCase().trim();
      document.querySelectorAll('.re-tree-node').forEach(node => {
        const name = node.querySelector('.re-node-name');
        if (!name) return;
        const match = !query || name.textContent.toLowerCase().includes(query);
        node.style.display = match ? '' : 'none';
        // Show parent nodes if child matches
        if (match && query) {
          let parent = node.parentElement;
          while (parent) {
            if (parent.classList && parent.classList.contains('re-tree-node')) {
              parent.style.display = '';
            }
            if (parent.classList && parent.classList.contains('re-children')) {
              parent.style.display = '';
            }
            parent = parent.parentElement;
          }
        }
      });
    });
  }
}

/**
 * Toggle tree node expand/collapse
 */
export function toggleTreeNode(el) {
  const nodeRow = el.closest ? el.closest('.re-node-row') : el;
  const treeNode = nodeRow.parentElement;
  const children = treeNode.querySelector('.re-children');
  const chevron = nodeRow.querySelector('.re-chevron');
  if (!children) return;

  const isCollapsed = children.style.display === 'none';
  children.style.display = isCollapsed ? '' : 'none';
  if (chevron) {
    chevron.classList.toggle('fa-chevron-down', isCollapsed);
    chevron.classList.toggle('fa-chevron-right', !isCollapsed);
  }
}

// Expose globally for inline onclick handlers
window.toggleTreeNode = toggleTreeNode;
