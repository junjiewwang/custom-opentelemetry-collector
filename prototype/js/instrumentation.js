/**
 * instrumentation.js — Instrumentation Page Logic
 * 
 * Responsibilities:
 * - Rule selection in left panel
 * - Detail tab switching
 * - Offline/completed targets toggle
 * - New Rule Modal (open/close/step navigation/create)
 * - Segmented controls, checkboxes, desired state toggle
 * - Toast notifications
 */

let modalCurrentStep = 1;
const MODAL_TOTAL_STEPS = 3;

/**
 * Initialize instrumentation page interactions
 */
export function initInstrumentationPage() {
  // Wire up modal overlay click-to-close
  const modal = document.getElementById('new-rule-modal');
  if (modal) {
    modal.addEventListener('click', (e) => {
      if (e.target === e.currentTarget) closeNewRuleModal();
    });
  }
}

// ---- Rule Selection ----
export function selectInstrumentationRule(index) {
  document.querySelectorAll('.inst-rule-item').forEach((item, i) => {
    item.classList.toggle('inst-rule-item--active', i === index);
  });
}

// ---- Detail Tab Switching ----
export function switchInstTab(tabKey) {
  document.querySelectorAll('#inst-tab-bar .inst-tab').forEach(btn => {
    btn.classList.toggle('inst-tab--active', btn.dataset.tab === tabKey);
  });
  document.querySelectorAll('.inst-tab-panel').forEach(panel => {
    panel.style.display = panel.id === `inst-panel-${tabKey}` ? 'block' : 'none';
  });
}

// ---- Offline/Completed Targets Toggle ----
export function toggleOfflineTargets() {
  const rows = document.querySelectorAll('.inst-offline-row');
  const chevron = document.getElementById('inst-offline-chevron');
  const isHidden = rows[0] && rows[0].style.display === 'none';

  rows.forEach(row => {
    row.style.display = isHidden ? '' : 'none';
  });

  if (chevron) {
    chevron.classList.toggle('inst-offline-chevron--expanded', isHidden);
  }
}

export function toggleCompletedTargets() {
  const rows = document.querySelectorAll('.inst-completed-row');
  const chevron = document.getElementById('inst-completed-chevron');
  const isHidden = rows[0] && rows[0].style.display === 'none';

  rows.forEach(row => {
    row.style.display = isHidden ? '' : 'none';
  });

  if (chevron) {
    chevron.classList.toggle('inst-offline-chevron--expanded', isHidden);
  }
}

export function toggleRuntimeOffline() {
  const group = document.getElementById('runtime-offline-group');
  const chevron = document.getElementById('runtime-offline-chevron');
  if (!group) return;
  const isHidden = group.style.display === 'none';
  group.style.display = isHidden ? '' : 'none';
  if (chevron) {
    chevron.style.transform = isHidden ? 'rotate(90deg)' : '';
  }
}

// ---- New Rule Modal ----
export function openNewRuleModal() {
  modalCurrentStep = 1;
  updateModalUI();
  document.getElementById('new-rule-modal').classList.add('modal-overlay--open');
  setTimeout(() => {
    const input = document.getElementById('rule-name-input');
    if (input) input.focus();
  }, 300);
}

export function closeNewRuleModal() {
  document.getElementById('new-rule-modal').classList.remove('modal-overlay--open');
}

export function modalNextStep() {
  if (modalCurrentStep < MODAL_TOTAL_STEPS) {
    modalCurrentStep++;
    updateModalUI();
  } else {
    createRule();
  }
}

export function modalPrevStep() {
  if (modalCurrentStep > 1) {
    modalCurrentStep--;
    updateModalUI();
  }
}

function updateModalUI() {
  for (let i = 1; i <= MODAL_TOTAL_STEPS; i++) {
    const section = document.getElementById('modal-step-' + i);
    if (section) {
      section.classList.toggle('modal-section--active', i === modalCurrentStep);
    }
  }

  document.querySelectorAll('.modal-step').forEach(step => {
    const stepNum = parseInt(step.dataset.step);
    step.classList.remove('modal-step--active', 'modal-step--done');
    if (stepNum === modalCurrentStep) {
      step.classList.add('modal-step--active');
    } else if (stepNum < modalCurrentStep) {
      step.classList.add('modal-step--done');
    }
  });

  const backBtn = document.getElementById('modal-back-btn');
  const nextLabel = document.getElementById('modal-next-label');
  const nextIcon = document.getElementById('modal-next-icon');

  backBtn.style.display = modalCurrentStep > 1 ? '' : 'none';

  if (modalCurrentStep === MODAL_TOTAL_STEPS) {
    nextLabel.textContent = 'Create Rule';
    nextIcon.className = 'fas fa-check';
  } else {
    nextLabel.textContent = 'Next';
    nextIcon.className = 'fas fa-arrow-right';
  }

  if (modalCurrentStep === MODAL_TOTAL_STEPS) {
    updateSummary();
  }
}

function updateSummary() {
  const name = document.getElementById('rule-name-input')?.value || '—';
  const classVal = document.getElementById('class-pattern-input')?.value || '—';
  const methodVal = document.getElementById('method-pattern-input')?.value || '—';
  const typeActive = document.querySelector('#rule-type-seg .segment--active');
  const type = typeActive ? typeActive.dataset.value : 'trace';

  document.getElementById('summary-name').textContent = name || '—';
  document.getElementById('summary-class').textContent = classVal || '—';
  document.getElementById('summary-method').textContent = methodVal || '—';
  document.getElementById('summary-type').textContent = type;
}

function createRule() {
  const nextBtn = document.getElementById('modal-next-btn');
  nextBtn.innerHTML = '<i class="fas fa-spinner fa-spin"></i> Creating...';
  nextBtn.disabled = true;

  setTimeout(() => {
    closeNewRuleModal();
    nextBtn.innerHTML = '<span id="modal-next-label">Next</span> <i class="fas fa-arrow-right" id="modal-next-icon"></i>';
    nextBtn.disabled = false;
    showToast('Rule created successfully');
  }, 800);
}

// ---- Segmented Control ----
export function selectSegment(el) {
  const parent = el.parentElement;
  parent.querySelectorAll('.segment').forEach(s => s.classList.remove('segment--active'));
  el.classList.add('segment--active');
}

// ---- Checkbox Toggle ----
export function toggleCheckbox(el) {
  el.classList.toggle('checkbox-item--checked');
}

// ---- Toggle Desired State ----
export function toggleDesiredState() {
  const track = document.getElementById('desired-state-toggle');
  const label = document.getElementById('desired-state-label');
  const isOn = track.classList.contains('toggle-track--on');

  track.classList.toggle('toggle-track--on', !isOn);
  label.classList.toggle('toggle-label--on', !isOn);
  label.textContent = isOn ? 'Paused' : 'Active';
}

// ---- Toast Notification ----
export function showToast(message) {
  let toast = document.getElementById('toast-notification');
  if (!toast) {
    toast = document.createElement('div');
    toast.id = 'toast-notification';
    toast.style.cssText = 'position:fixed;bottom:24px;right:24px;padding:10px 20px;background:var(--accent-green-dim);color:#fff;border-radius:var(--radius-md);font-size:0.8rem;font-weight:500;z-index:600;display:flex;align-items:center;gap:8px;box-shadow:var(--shadow-lg);animation:modalSlideIn 0.3s ease';
    document.body.appendChild(toast);
  }
  toast.innerHTML = '<i class="fas fa-check-circle"></i> ' + message;
  toast.style.display = 'flex';
  setTimeout(() => { toast.style.display = 'none'; }, 3000);
}

// Expose globally for inline onclick handlers
window.selectInstrumentationRule = selectInstrumentationRule;
window.switchInstTab = switchInstTab;
window.toggleOfflineTargets = toggleOfflineTargets;
window.toggleCompletedTargets = toggleCompletedTargets;
window.toggleRuntimeOffline = toggleRuntimeOffline;
window.openNewRuleModal = openNewRuleModal;
window.closeNewRuleModal = closeNewRuleModal;
window.modalNextStep = modalNextStep;
window.modalPrevStep = modalPrevStep;
window.selectSegment = selectSegment;
window.toggleCheckbox = toggleCheckbox;
window.toggleDesiredState = toggleDesiredState;
window.showToast = showToast;

// Register for keyboard handler
window.__apm_modules = window.__apm_modules || {};
window.__apm_modules['./instrumentation.js'] = { closeNewRuleModal };
