import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { apiClient } from '@/api/client';
import EmptyState from '@/components/EmptyState';
import InstrumentationRuleEditor, { type InstrumentationRuleEditorPayload } from '@/components/InstrumentationRuleEditor';
import SearchableSelect, { type SelectOption } from '@/components/SearchableSelect';
import { useConfirm } from '@/components/ConfirmDialog';
import { useToast } from '@/contexts/ToastContext';
import type { ApiError, App, Instance, ServiceDetail } from '@/types/api';
import type {
  InstrumentationAuditAction,
  InstrumentationAuditSource,
  InstrumentationAuditStatus,
  InstrumentationInstrumentType,
  InstrumentationRule,
  InstrumentationRuleAuditEntry,
  InstrumentationRuleDesiredState,
  InstrumentationRuntimeDriftReason,
  InstrumentationRuleRuntimeSnapshot,
  InstrumentationRuleRuntimeSnapshotTarget,
  InstrumentationRuleTargetStatus,
  InstrumentationRuntimeRefreshStatus,
  InstrumentationTargetState,
} from '@/types/instrumentation';

type RuleStateFilter = 'all' | InstrumentationRuleDesiredState;
type RuleTypeFilter = 'all' | InstrumentationInstrumentType;

const RULE_STATE_FILTERS: { value: RuleStateFilter; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'paused', label: 'Paused' },
  { value: 'deleted', label: 'Deleted' },
];

const RULE_TYPE_FILTERS: { value: RuleTypeFilter; label: string }[] = [
  { value: 'all', label: 'All types' },
  { value: 'trace', label: 'Trace' },
  { value: 'metric', label: 'Metric' },
  { value: 'log', label: 'Log' },
];

function formatTimestamp(ts?: number): string {
  if (!ts) return '-';
  return new Date(ts).toLocaleString('zh-CN');
}

function formatRelativeTime(ts?: number): string {
  if (!ts) return '-';
  const diff = Date.now() - ts;
  if (diff < 30_000) return 'just now';
  const minutes = Math.floor(diff / 60_000);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  return formatTimestamp(ts);
}

function ruleStatusClass(status: string): string {
  switch (status) {
    case 'success':
      return 'bg-green-50 text-green-700 ring-green-200';
    case 'running':
      return 'bg-blue-50 text-blue-700 ring-blue-200';
    case 'partial_success':
      return 'bg-amber-50 text-amber-700 ring-amber-200';
    case 'failed':
      return 'bg-red-50 text-red-700 ring-red-200';
    default:
      return 'bg-gray-100 text-gray-600 ring-gray-200';
  }
}

function targetStateClass(state: InstrumentationTargetState): string {
  switch (state) {
    case 'applied':
    case 'removed':
      return 'bg-green-50 text-green-700 ring-green-200';
    case 'running':
      return 'bg-blue-50 text-blue-700 ring-blue-200';
    case 'dispatched':
    case 'pending':
      return 'bg-gray-100 text-gray-600 ring-gray-200';
    case 'offline':
      return 'bg-slate-100 text-slate-600 ring-slate-200';
    case 'expired':
      return 'bg-amber-50 text-amber-700 ring-amber-200';
    case 'failed':
      return 'bg-red-50 text-red-700 ring-red-200';
    default:
      return 'bg-gray-100 text-gray-600 ring-gray-200';
  }
}

const OFFLINE_STATES: InstrumentationTargetState[] = ['offline', 'expired'];
const COMPLETED_STATES: InstrumentationTargetState[] = ['removed'];

function isOfflineTarget(target: InstrumentationRuleTargetStatus): boolean {
  return OFFLINE_STATES.includes(target.state);
}

function isCompletedTarget(target: InstrumentationRuleTargetStatus): boolean {
  return COMPLETED_STATES.includes(target.state);
}

function isActiveTarget(target: InstrumentationRuleTargetStatus): boolean {
  return !isOfflineTarget(target) && !isCompletedTarget(target);
}

/** Target Status 表格行 */
function TargetRow({ target }: { target: InstrumentationRuleTargetStatus }) {
  return (
    <tr className="align-top">
      <td className="px-4 py-3">
        <div className="font-medium text-gray-800 break-all">{target.agent_id}</div>
        <div className="text-xs text-gray-400 mt-1">desired: {target.desired_state}</div>
      </td>
      <td className="px-4 py-3 text-gray-600">
        <div>{target.hostname || '-'}</div>
        <div className="text-xs text-gray-400 mt-1">{target.ip || '-'}</div>
      </td>
      <td className="px-4 py-3">
        <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${targetStateClass(target.state)}`}>
          {target.state}
        </span>
        {target.task_status && (
          <div className="text-xs text-gray-400 mt-1">task: {target.task_status}</div>
        )}
      </td>
      <td className="px-4 py-3 text-gray-600">
        <div>{target.task_type || '-'}</div>
        <div className="text-xs text-gray-400 mt-1 break-all">{target.task_id || '-'}</div>
        <div className="text-xs text-gray-400 mt-1">dispatch: {formatRelativeTime(target.last_dispatch_at_millis)}</div>
      </td>
      <td className="px-4 py-3 text-gray-500 max-w-xs">
        <div className="break-words text-xs leading-5">{target.last_error_message || '-'}</div>
      </td>
      <td className="px-4 py-3 text-gray-500 whitespace-nowrap">
        {formatRelativeTime(target.updated_at_millis)}
      </td>
    </tr>
  );
}

/** 折叠分组行 */
function CollapsibleGroupRow({
  expanded,
  onToggle,
  icon,
  iconColor,
  label,
  count,
  hint,
}: {
  expanded: boolean;
  onToggle: () => void;
  icon: string;
  iconColor: string;
  label: string;
  count: number;
  hint?: string;
}) {
  return (
    <tr className="bg-slate-50/60">
      <td colSpan={6} className="px-4 py-2">
        <button
          onClick={onToggle}
          className="flex items-center gap-2 text-xs font-medium text-slate-500 hover:text-slate-700 transition-colors w-full"
        >
          <i className={`fas fa-chevron-right text-[10px] transition-transform duration-200 ${expanded ? 'rotate-90' : ''}`} />
          <i className={`${icon} ${iconColor}`} />
          <span>{label} ({count})</span>
          {hint && <span className="ml-1 text-slate-400 font-normal">— {hint}</span>}
          <span className="ml-auto text-slate-400 font-normal">
            {expanded ? 'click to collapse' : 'click to expand'}
          </span>
        </button>
      </td>
    </tr>
  );
}

/** Target Status 三段分组表格：Active → Completed/Removed → Offline/Expired */
function TargetStatusTable({ targets, rulePaused }: { targets: InstrumentationRuleTargetStatus[]; rulePaused?: boolean }) {
  const [completedExpanded, setCompletedExpanded] = useState(false);
  const [offlineExpanded, setOfflineExpanded] = useState(false);

  const activeTargets = useMemo(() => targets.filter(isActiveTarget), [targets]);
  const completedTargets = useMemo(() => targets.filter(isCompletedTarget), [targets]);
  const offlineTargets = useMemo(() => targets.filter(isOfflineTarget), [targets]);

  // 最近一次 completed target 的活动时间
  const completedHint = useMemo(() => {
    if (completedTargets.length === 0) return undefined;
    const latest = Math.max(...completedTargets.map(t => t.updated_at_millis));
    return `last activity: ${formatRelativeTime(latest)}`;
  }, [completedTargets]);

  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-gray-100 text-sm">
        <thead className="bg-gray-50/80">
          <tr className="text-left text-xs uppercase tracking-wide text-gray-400">
            <th className="px-4 py-3 font-medium">Agent</th>
            <th className="px-4 py-3 font-medium">Host</th>
            <th className="px-4 py-3 font-medium">State</th>
            <th className="px-4 py-3 font-medium">Task</th>
            <th className="px-4 py-3 font-medium">Error</th>
            <th className="px-4 py-3 font-medium">Updated</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100 bg-white">
          {/* ── Active Targets ── */}
          {activeTargets.length > 0 ? (
            activeTargets.map(target => (
              <TargetRow key={`${target.rule_id}:${target.agent_id}`} target={target} />
            ))
          ) : (
            <tr>
              <td colSpan={6} className="px-4 py-6 text-center">
                <div className="text-gray-400 text-sm">
                  <i className="fas fa-info-circle mr-2" />
                  {rulePaused
                    ? 'Rule is paused — all targets have been uninstrumented'
                    : 'No active targets'
                  }
                </div>
              </td>
            </tr>
          )}

          {/* ── Completed / Removed ── */}
          {completedTargets.length > 0 && (
            <>
              <CollapsibleGroupRow
                expanded={completedExpanded}
                onToggle={() => setCompletedExpanded(!completedExpanded)}
                icon="fas fa-check-circle"
                iconColor="text-gray-400"
                label="Completed / Removed"
                count={completedTargets.length}
                hint={completedHint}
              />
              {completedExpanded && completedTargets.map(target => (
                <TargetRow key={`${target.rule_id}:${target.agent_id}`} target={target} />
              ))}
            </>
          )}

          {/* ── Offline / Expired ── */}
          {offlineTargets.length > 0 && (
            <>
              <CollapsibleGroupRow
                expanded={offlineExpanded}
                onToggle={() => setOfflineExpanded(!offlineExpanded)}
                icon="fas fa-plug-circle-xmark"
                iconColor="text-slate-400"
                label="Offline / Expired"
                count={offlineTargets.length}
              />
              {offlineExpanded && offlineTargets.map(target => (
                <TargetRow key={`${target.rule_id}:${target.agent_id}`} target={target} />
              ))}
            </>
          )}
        </tbody>
      </table>
    </div>
  );
}

// ─── Health Summary Bar ──────────────────────────────────────────────────────

interface HealthSummaryBarProps {
  rule: InstrumentationRule;
  runtimeSnapshot: InstrumentationRuleRuntimeSnapshot | null;
}

function HealthSummaryBar({ rule, runtimeSnapshot }: HealthSummaryBarProps) {
  const { summary } = rule;

  // Active targets = total - offline - expired (removed are "completed", not active)
  // For progress bar we use active scope only
  const activeTotal = summary.applied_targets + summary.running_targets + summary.pending_targets + summary.failed_targets;
  const denominator = activeTotal || 1; // avoid divide by zero

  // Segment widths (percentage) — based on active targets only
  const applied = (summary.applied_targets / denominator) * 100;
  const running = ((summary.running_targets + summary.pending_targets) / denominator) * 100;
  const failed = (summary.failed_targets / denominator) * 100;

  // Coverage: applied out of active targets
  const coveragePercent = activeTotal > 0 ? Math.round((summary.applied_targets / activeTotal) * 100) : 0;

  // Inactive counts for context
  const inactiveCount = summary.total_targets - activeTotal;

  // Determine health verdict
  let verdict: { label: string; color: string; icon: string };
  if (rule.desired_state === 'paused') {
    verdict = { label: 'Paused', color: 'text-amber-600', icon: 'fas fa-pause-circle' };
  } else if (rule.desired_state === 'deleted') {
    verdict = { label: 'Deleted', color: 'text-gray-500', icon: 'fas fa-trash' };
  } else if (summary.failed_targets > 0) {
    verdict = { label: 'Degraded', color: 'text-red-600', icon: 'fas fa-exclamation-circle' };
  } else if (activeTotal === 0) {
    verdict = { label: 'No Active Targets', color: 'text-gray-500', icon: 'fas fa-minus-circle' };
  } else if (coveragePercent === 100) {
    verdict = { label: 'Healthy', color: 'text-green-600', icon: 'fas fa-check-circle' };
  } else if (coveragePercent >= 50) {
    verdict = { label: 'Partial', color: 'text-amber-600', icon: 'fas fa-exclamation-triangle' };
  } else {
    verdict = { label: 'Low Coverage', color: 'text-orange-600', icon: 'fas fa-minus-circle' };
  }

  return (
    <div className="rounded-xl border border-gray-200 bg-gradient-to-r from-gray-50/60 to-white px-5 py-4">
      <div className="flex items-center justify-between mb-3">
        {/* Verdict */}
        <div className="flex items-center gap-2">
          <i className={`${verdict.icon} ${verdict.color}`} />
          <span className={`text-sm font-semibold ${verdict.color}`}>{verdict.label}</span>
          <span className="text-xs text-gray-400 ml-2">
            {summary.applied_targets}/{activeTotal} active applied
          </span>
          {inactiveCount > 0 && (
            <span className="text-[10px] text-gray-300 ml-1">
              (+{inactiveCount} inactive)
            </span>
          )}
        </div>

        {/* Coverage badge + last reconcile */}
        <div className="flex items-center gap-3">
          {activeTotal > 0 && (
            <span className="px-2.5 py-1 rounded-full bg-blue-50 text-blue-700 text-[11px] font-bold ring-1 ring-blue-200">
              {coveragePercent}% coverage
            </span>
          )}
          {runtimeSnapshot && (
            <span className="text-[11px] text-gray-400">
              <i className="fas fa-clock mr-1" />
              snapshot {formatRelativeTime(runtimeSnapshot.generated_at_millis)}
            </span>
          )}
        </div>
      </div>

      {/* Segmented progress bar — active targets only */}
      <div className="w-full h-2.5 rounded-full bg-gray-100 overflow-hidden flex">
        {applied > 0 && (
          <div
            className="h-full bg-green-500 transition-all duration-500"
            style={{ width: `${applied}%` }}
            title={`Applied: ${summary.applied_targets}`}
          />
        )}
        {running > 0 && (
          <div
            className="h-full bg-blue-400 transition-all duration-500"
            style={{ width: `${running}%` }}
            title={`Running/Pending: ${summary.running_targets + summary.pending_targets}`}
          />
        )}
        {failed > 0 && (
          <div
            className="h-full bg-red-500 transition-all duration-500"
            style={{ width: `${failed}%` }}
            title={`Failed: ${summary.failed_targets}`}
          />
        )}
      </div>

      {/* Legend */}
      <div className="flex items-center gap-4 mt-2 text-[10px] text-gray-400">
        <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-green-500" />Applied ({summary.applied_targets})</span>
        <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-blue-400" />Running/Pending ({summary.running_targets + summary.pending_targets})</span>
        <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-red-500" />Failed ({summary.failed_targets})</span>
        {inactiveCount > 0 && (
          <span className="flex items-center gap-1 ml-auto"><span className="w-2 h-2 rounded-full bg-gray-300" />Inactive ({inactiveCount})</span>
        )}
      </div>
    </div>
  );
}

// ─── Detail Tabs ─────────────────────────────────────────────────────────────

type DetailTab = 'targets' | 'runtime' | 'config' | 'audit';

interface DetailTabsProps {
  rule: InstrumentationRule;
  selectedApp: App | null;
  targets: InstrumentationRuleTargetStatus[];
  targetsLoading: boolean;
  runtimeSnapshot: InstrumentationRuleRuntimeSnapshot | null;
  runtimeSnapshotLoading: boolean;
}

function DetailTabs({ rule, selectedApp, targets, targetsLoading, runtimeSnapshot, runtimeSnapshotLoading }: DetailTabsProps) {
  const [activeTab, setActiveTab] = useState<DetailTab>('targets');

  const activeTargetCount = useMemo(() => targets.filter(isActiveTarget).length, [targets]);

  const tabs: { key: DetailTab; label: string; icon: string; badge?: number }[] = [
    { key: 'targets', label: 'Targets', icon: 'fas fa-crosshairs', badge: activeTargetCount || targets.length },
    { key: 'runtime', label: 'Runtime', icon: 'fas fa-satellite-dish', badge: runtimeSnapshot?.targets.length },
    { key: 'config', label: 'Config & Op', icon: 'fas fa-sliders-h' },
    { key: 'audit', label: 'Audit', icon: 'fas fa-history', badge: rule.recent_audits?.length },
  ];

  return (
    <div className="rounded-xl border border-gray-200 bg-white overflow-hidden">
      {/* Tab bar */}
      <div className="flex border-b border-gray-100 bg-gray-50/50">
        {tabs.map(tab => {
          const isActive = activeTab === tab.key;
          return (
            <button
              key={tab.key}
              onClick={() => setActiveTab(tab.key)}
              className={`flex items-center gap-2 px-4 py-3 text-xs font-medium border-b-2 transition ${
                isActive
                  ? 'border-blue-500 text-blue-700 bg-white'
                  : 'border-transparent text-gray-500 hover:text-gray-700 hover:bg-gray-50'
              }`}
            >
              <i className={`${tab.icon} text-[10px]`} />
              {tab.label}
              {tab.badge !== undefined && tab.badge > 0 && (
                <span className={`px-1.5 py-0.5 rounded-full text-[10px] font-bold ${
                  isActive ? 'bg-blue-100 text-blue-700' : 'bg-gray-200 text-gray-600'
                }`}>
                  {tab.badge}
                </span>
              )}
            </button>
          );
        })}
      </div>

      {/* Tab panels */}
      <div className="p-4">
        {activeTab === 'targets' && (
          <TabPanelTargets targets={targets} loading={targetsLoading} rulePaused={rule.desired_state === 'paused'} />
        )}
        {activeTab === 'runtime' && (
          <TabPanelRuntime snapshot={runtimeSnapshot} loading={runtimeSnapshotLoading} />
        )}
        {activeTab === 'config' && (
          <TabPanelConfig rule={rule} selectedApp={selectedApp} />
        )}
        {activeTab === 'audit' && (
          <TabPanelAudit audits={rule.recent_audits} />
        )}
      </div>
    </div>
  );
}

// ─── Tab Panel: Targets ──────────────────────────────────────────────────────

function TabPanelTargets({ targets, loading, rulePaused }: { targets: InstrumentationRuleTargetStatus[]; loading: boolean; rulePaused?: boolean }) {
  if (loading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="h-12 skeleton-shimmer rounded-lg" />
        ))}
      </div>
    );
  }

  if (targets.length === 0) {
    return (
      <div className="text-center py-8 text-gray-400 text-sm">
        <i className="fas fa-crosshairs text-2xl mb-2 block" />
        No targets yet
      </div>
    );
  }

  return <TargetStatusTable targets={targets} rulePaused={rulePaused} />;
}

// ─── Tab Panel: Runtime Snapshot ─────────────────────────────────────────────

function TabPanelRuntime({ snapshot, loading }: { snapshot: InstrumentationRuleRuntimeSnapshot | null; loading: boolean }) {
  const [showOffline, setShowOffline] = React.useState(false);

  if (loading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="h-12 skeleton-shimmer rounded-lg" />
        ))}
      </div>
    );
  }

  if (!snapshot) {
    return (
      <div className="text-center py-8 text-gray-400 text-sm">
        <i className="fas fa-satellite-dish text-2xl mb-2 block" />
        No runtime snapshot available
      </div>
    );
  }

  const { summary: s, targets: snapshotTargets } = snapshot;

  // Split targets into reachable (online) vs offline groups
  const isOfflineTarget = (t: InstrumentationRuleRuntimeSnapshotTarget) =>
    t.controlplane_state === 'offline' || t.controlplane_state === 'expired' ||
    (t.last_refresh_status === 'skipped' && t.last_error_message === 'agent is offline');

  const reachableTargets = snapshotTargets.filter(t => !isOfflineTarget(t));
  const offlineTargets = snapshotTargets.filter(t => isOfflineTarget(t));

  return (
    <div className="space-y-4">
      {/* Summary metrics — scoped to reachable targets only */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <RuntimeMetric label="Reachable" value={s.reachable_targets || reachableTargets.length} />
        <RuntimeMetric label="Effective" value={s.effective_targets} color="text-green-600" />
        <RuntimeMetric label="Drifted" value={s.drifted_targets} color="text-amber-600" />
        <RuntimeMetric label="Missing" value={s.missing_targets} color="text-red-600" />
      </div>

      {/* Reachable targets table */}
      {reachableTargets.length > 0 && (
        <div className="overflow-x-auto">
          <table className="min-w-full divide-y divide-gray-100 text-xs">
            <thead className="bg-gray-50/80">
              <tr className="text-left uppercase tracking-wide text-gray-400">
                <th className="px-3 py-2 font-medium">Agent</th>
                <th className="px-3 py-2 font-medium">Effective</th>
                <th className="px-3 py-2 font-medium">Refresh</th>
                <th className="px-3 py-2 font-medium">Drift</th>
                <th className="px-3 py-2 font-medium">Diagnostic</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {reachableTargets.map(t => (
                <RuntimeTargetRow key={t.agent_id} target={t} isOffline={false} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {reachableTargets.length === 0 && (
        <div className="text-center py-4 text-gray-400 text-xs">
          <i className="fas fa-satellite-dish mr-1" />
          No reachable agents — all targets are offline
        </div>
      )}

      {/* Offline targets — collapsible */}
      {offlineTargets.length > 0 && (
        <div className="border-t border-gray-100 pt-3">
          <button
            onClick={() => setShowOffline(!showOffline)}
            className="flex items-center gap-2 text-xs text-gray-400 hover:text-gray-600 transition-colors"
          >
            <i className={`fas fa-chevron-${showOffline ? 'down' : 'right'} text-[9px]`} />
            <i className="fas fa-wifi-slash text-[10px] opacity-60" />
            <span>Offline / Expired ({offlineTargets.length})</span>
            <span className="text-[10px] italic ml-1">— stale data, agent unreachable</span>
          </button>
          {showOffline && (
            <div className="mt-2 overflow-x-auto">
              <table className="min-w-full divide-y divide-gray-100 text-xs opacity-60">
                <thead className="bg-gray-50/50">
                  <tr className="text-left uppercase tracking-wide text-gray-300">
                    <th className="px-3 py-2 font-medium">Agent</th>
                    <th className="px-3 py-2 font-medium">Effective</th>
                    <th className="px-3 py-2 font-medium">Refresh</th>
                    <th className="px-3 py-2 font-medium">Drift</th>
                    <th className="px-3 py-2 font-medium">Diagnostic</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-50">
                  {offlineTargets.map(t => (
                    <RuntimeTargetRow key={t.agent_id} target={t} isOffline={true} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function RuntimeTargetRow({ target: t, isOffline }: { target: InstrumentationRuleRuntimeSnapshotTarget; isOffline: boolean }) {
  return (
    <tr className={isOffline ? 'opacity-70' : 'hover:bg-gray-50/50'}>
      <td className="px-3 py-2">
        <div className={`font-medium ${isOffline ? 'text-gray-400' : 'text-gray-700'}`}>{t.agent_id}</div>
        <div className="text-gray-400">{t.hostname || t.ip || '-'}</div>
      </td>
      <td className="px-3 py-2">
        {isOffline ? (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-gray-50 text-gray-400 ring-1 ring-gray-200 text-[10px] font-semibold">
            <i className="fas fa-minus text-[8px]" /> offline
          </span>
        ) : t.is_effective ? (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-green-50 text-green-700 ring-1 ring-green-200 text-[10px] font-semibold">
            <i className="fas fa-check text-[8px]" /> yes
          </span>
        ) : (
          <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-red-50 text-red-700 ring-1 ring-red-200 text-[10px] font-semibold">
            <i className="fas fa-times text-[8px]" /> no
          </span>
        )}
      </td>
      <td className="px-3 py-2">
        <span className={`inline-flex px-2 py-0.5 rounded-full text-[10px] font-semibold ring-1 ${runtimeRefreshStatusClass(t.last_refresh_status)}`}>
          {t.last_refresh_status || 'idle'}
        </span>
      </td>
      <td className="px-3 py-2">
        {!isOffline && t.drift_reasons && t.drift_reasons.length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {t.drift_reasons.map(reason => (
              <span key={reason} className={`inline-flex px-2 py-0.5 rounded-full text-[10px] font-semibold ring-1 ${driftReasonClass(reason)}`}>
                {formatDriftReason(reason)}
              </span>
            ))}
          </div>
        ) : (
          <span className="text-gray-400">-</span>
        )}
      </td>
      <td className="px-3 py-2 text-gray-500 max-w-[200px]">
        <div className="truncate">{t.diagnostic_message || t.last_error_message || '-'}</div>
      </td>
    </tr>
  );
}

function RuntimeMetric({ label, value, color = 'text-gray-800' }: { label: string; value: number; color?: string }) {
  return (
    <div className="rounded-lg border border-gray-100 bg-gray-50/40 px-3 py-2 text-center">
      <div className={`text-lg font-bold ${color}`}>{value}</div>
      <div className="text-[10px] text-gray-400 mt-0.5">{label}</div>
    </div>
  );
}

// ─── Tab Panel: Config & Operation ───────────────────────────────────────────

function TabPanelConfig({ rule, selectedApp }: { rule: InstrumentationRule; selectedApp: App | null }) {
  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      {/* Rule Configuration */}
      <div className="rounded-lg border border-gray-100 bg-gray-50/30 p-4">
        <h4 className="text-xs font-semibold text-gray-700 uppercase tracking-wide mb-3">
          <i className="fas fa-cog mr-2 text-gray-400" />Rule Configuration
        </h4>
        <dl className="space-y-2 text-xs">
          <ConfigRow label="Class" value={rule.class_name} />
          <ConfigRow label="Method" value={rule.method_name} />
          <ConfigRow label="Type" value={rule.instrument_type} />
          <ConfigRow label="Scope" value={rule.scope_type} />
          {rule.span_name && <ConfigRow label="Span Name" value={rule.span_name} />}
          {rule.parameter_types && <ConfigRow label="Params" value={rule.parameter_types} />}
          {rule.capture_args && <ConfigRow label="Capture Args" value={rule.capture_args} />}
          {rule.capture_return && <ConfigRow label="Capture Return" value={rule.capture_return} />}
          {rule.force && <ConfigRow label="Force" value="Yes" />}
          <ConfigRow label="App" value={selectedApp?.name || rule.app_id} />
          <ConfigRow label="Service" value={rule.service_name} />
          <ConfigRow label="Created" value={formatTimestamp(rule.created_at_millis)} />
          <ConfigRow label="Updated" value={formatTimestamp(rule.updated_at_millis)} />
        </dl>
      </div>

      {/* Last Operation */}
      <div className="rounded-lg border border-gray-100 bg-gray-50/30 p-4">
        <h4 className="text-xs font-semibold text-gray-700 uppercase tracking-wide mb-3">
          <i className="fas fa-play-circle mr-2 text-gray-400" />Last Operation
        </h4>
        {rule.last_operation ? (
          <dl className="space-y-2 text-xs">
            <ConfigRow label="Type" value={rule.last_operation.type} />
            <ConfigRow label="Status" value={rule.last_operation.status} />
            <ConfigRow label="Started" value={formatTimestamp(rule.last_operation.started_at_millis)} />
            {rule.last_operation.completed_at_millis && (
              <ConfigRow label="Completed" value={formatTimestamp(rule.last_operation.completed_at_millis)} />
            )}
            <ConfigRow label="Total" value={String(rule.last_operation.total_targets)} />
            <ConfigRow label="Applied" value={String(rule.last_operation.applied_targets)} />
            <ConfigRow label="Failed" value={String(rule.last_operation.failed_targets)} />
          </dl>
        ) : (
          <p className="text-gray-400 text-xs italic">No operations recorded yet.</p>
        )}
      </div>
    </div>
  );
}

function ConfigRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start gap-2">
      <dt className="w-24 flex-shrink-0 text-gray-400 font-medium">{label}</dt>
      <dd className="text-gray-700 break-all">{value}</dd>
    </div>
  );
}

// ─── Tab Panel: Audit Log ────────────────────────────────────────────────────

function TabPanelAudit({ audits }: { audits?: InstrumentationRuleAuditEntry[] }) {
  if (!audits || audits.length === 0) {
    return (
      <div className="text-center py-8 text-gray-400 text-sm">
        <i className="fas fa-history text-2xl mb-2 block" />
        No audit entries yet
      </div>
    );
  }

  return (
    <div className="space-y-0.5">
      {audits.map(entry => (
        <div key={entry.audit_id} className="flex items-start gap-3 py-2.5 px-3 rounded-lg hover:bg-gray-50/60 transition">
          {/* Timeline dot */}
          <div className="flex-shrink-0 mt-1">
            <span className={`w-2.5 h-2.5 rounded-full inline-block ring-2 ring-white ${
              entry.status === 'success' ? 'bg-green-500' : entry.status === 'failed' ? 'bg-red-500' : 'bg-amber-400'
            }`} />
          </div>
          {/* Content */}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 flex-wrap">
              <span className={`inline-flex px-2 py-0.5 rounded-full text-[10px] font-semibold ring-1 ${auditStatusClass(entry.status)}`}>
                {entry.status}
              </span>
              <span className="text-xs font-medium text-gray-700">{formatAuditAction(entry.action)}</span>
              <span className="text-[10px] text-gray-400 px-1.5 py-0.5 rounded bg-gray-100">
                {formatAuditSource(entry.source)}
              </span>
            </div>
            <div className="mt-1 flex items-center gap-3 text-[11px] text-gray-400">
              {entry.agent_id && <span><i className="fas fa-server mr-1" />{entry.agent_id}</span>}
              <span><i className="fas fa-clock mr-1" />{formatTimestamp(entry.created_at_millis)}</span>
            </div>
            {entry.message && (
              <p className="mt-1 text-xs text-gray-500 break-all">{entry.message}</p>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

// ─── Helper Functions ────────────────────────────────────────────────────────

function runtimeRefreshStatusClass(status?: InstrumentationRuntimeRefreshStatus): string {
  switch (status) {
    case 'success':
      return 'bg-green-50 text-green-700 ring-green-200';
    case 'failed':
    case 'timeout':
      return 'bg-red-50 text-red-700 ring-red-200';
    case 'skipped':
      return 'bg-amber-50 text-amber-700 ring-amber-200';
    default:
      return 'bg-gray-100 text-gray-600 ring-gray-200';
  }
}

function driftReasonClass(reason: InstrumentationRuntimeDriftReason): string {
  switch (reason) {
    case 'missing':
      return 'bg-red-50 text-red-700 ring-red-200';
    case 'ineffective':
      return 'bg-amber-50 text-amber-700 ring-amber-200';
    case 'deleted_residual':
    case 'paused_residual':
      return 'bg-orange-50 text-orange-700 ring-orange-200';
    case 'instrumentation_unavailable':
    case 'enhancement_unavailable':
      return 'bg-slate-100 text-slate-700 ring-slate-200';
    default:
      return 'bg-gray-100 text-gray-600 ring-gray-200';
  }
}

function formatDriftReason(reason: InstrumentationRuntimeDriftReason): string {
  switch (reason) {
    case 'missing':
      return 'missing';
    case 'ineffective':
      return 'ineffective';
    case 'deleted_residual':
      return 'deleted residual';
    case 'paused_residual':
      return 'paused residual';
    case 'instrumentation_unavailable':
      return 'instrumentation unavailable';
    case 'enhancement_unavailable':
      return 'enhancement unavailable';
    default:
      return reason;
  }
}

function auditStatusClass(status: InstrumentationAuditStatus): string {
  switch (status) {
    case 'success':
      return 'bg-green-50 text-green-700 ring-green-200';
    case 'failed':
      return 'bg-red-50 text-red-700 ring-red-200';
    default:
      return 'bg-amber-50 text-amber-700 ring-amber-200';
  }
}

function formatAuditAction(action: InstrumentationAuditAction): string {
  switch (action) {
    case 'target_discovered':
      return 'target discovered';
    case 'target_pruned':
      return 'target pruned';
    default:
      return action;
  }
}

function formatAuditSource(source: InstrumentationAuditSource): string {
  return source === 'reconcile' ? 'reconcile' : 'manual';
}

function summarizeRule(rule: InstrumentationRule): string {
  return `${rule.class_name}.${rule.method_name}`;
}

export default function InstrumentationPage() {
  const { showToast } = useToast();
  const confirm = useConfirm();
  const [searchParams, setSearchParams] = useSearchParams();
  const initialAppIdRef = useRef(searchParams.get('app_id') || '');
  const initialServiceNameRef = useRef(searchParams.get('service_name') || '');

  const [apps, setApps] = useState<App[]>([]);
  const [services, setServices] = useState<ServiceDetail[]>([]);
  const [availableAgents, setAvailableAgents] = useState<Instance[]>([]);
  const [rules, setRules] = useState<InstrumentationRule[]>([]);
  const [targets, setTargets] = useState<InstrumentationRuleTargetStatus[]>([]);
  const [runtimeSnapshot, setRuntimeSnapshot] = useState<InstrumentationRuleRuntimeSnapshot | null>(null);

  const [appsLoading, setAppsLoading] = useState(false);
  const [servicesLoading, setServicesLoading] = useState(false);
  const [rulesLoading, setRulesLoading] = useState(false);
  const [targetsLoading, setTargetsLoading] = useState(false);
  const [runtimeSnapshotLoading, setRuntimeSnapshotLoading] = useState(false);
  const [runtimeSnapshotRefreshing, setRuntimeSnapshotRefreshing] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const [selectedAppId, setSelectedAppId] = useState('');
  const [selectedServiceName, setSelectedServiceName] = useState('');
  const [selectedRule, setSelectedRule] = useState<InstrumentationRule | null>(null);

  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState<RuleStateFilter>('all');
  const [typeFilter, setTypeFilter] = useState<RuleTypeFilter>('all');

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<InstrumentationRule | null>(null);

  const loadTargets = useCallback(async (ruleId: string) => {
    setTargetsLoading(true);
    try {
      const data = await apiClient.getInstrumentationTargets(ruleId);
      setTargets(data);
    } catch (e) {
      showToast(`Failed to load targets: ${(e as ApiError).message}`, 'error');
      setTargets([]);
    } finally {
      setTargetsLoading(false);
    }
  }, [showToast]);

  const loadRuntimeSnapshot = useCallback(async (ruleId: string, force = false) => {
    if (!ruleId) {
      setRuntimeSnapshot(null);
      return;
    }

    if (force) setRuntimeSnapshotRefreshing(true);
    else setRuntimeSnapshotLoading(true);

    try {
      const data = force
        ? await apiClient.refreshInstrumentationRuntimeSnapshot(ruleId)
        : await apiClient.getInstrumentationRuntimeSnapshot(ruleId);
      setRuntimeSnapshot(data);
    } catch (e) {
      showToast(`Failed to load runtime snapshot: ${(e as ApiError).message}`, 'error');
      if (!force) setRuntimeSnapshot(null);
    } finally {
      if (force) setRuntimeSnapshotRefreshing(false);
      else setRuntimeSnapshotLoading(false);
    }
  }, [showToast]);

  const loadRules = useCallback(async (appId: string, serviceName?: string) => {
    if (!appId) {
      setRules([]);
      setSelectedRule(null);
      return;
    }

    setRulesLoading(true);
    try {
      const data = await apiClient.listInstrumentationRules({
        app_id: appId,
        service_name: serviceName || undefined,
        include_deleted: true,
      });
      setRules(data);
      setSelectedRule(prev => {
        if (prev) {
          return data.find(item => item.rule_id === prev.rule_id) || (data[0] ?? null);
        }
        return data[0] ?? null;
      });
    } catch (e) {
      showToast(`Failed to load rules: ${(e as ApiError).message}`, 'error');
      setRules([]);
      setSelectedRule(null);
    } finally {
      setRulesLoading(false);
    }
  }, [showToast]);

  const loadAvailableAgents = useCallback(async (appId: string, serviceName?: string) => {
    if (!appId || !serviceName) {
      setAvailableAgents([]);
      return;
    }
    try {
      const data = await apiClient.getInstances('', {
        status: 'all',
        app_id: appId,
        service_name: serviceName,
      });
      setAvailableAgents(data);
    } catch (e) {
      showToast(`Failed to load instances: ${(e as ApiError).message}`, 'error');
      setAvailableAgents([]);
    }
  }, [showToast]);

  const loadServices = useCallback(async (appId: string, preferredServiceName?: string) => {
    if (!appId) {
      setServices([]);
      setSelectedServiceName('');
      setAvailableAgents([]);
      setRules([]);
      setSelectedRule(null);
      return;
    }

    setServicesLoading(true);
    try {
      const serviceList = await apiClient.getAppServices(appId);
      setServices(serviceList);
      if (serviceList.length === 0) {
        setSelectedServiceName('');
        setAvailableAgents([]);
        setRules([]);
        setSelectedRule(null);
        return;
      }

      const preferred = preferredServiceName && serviceList.some(item => item.service_name === preferredServiceName)
        ? preferredServiceName
        : serviceList[0]!.service_name;

      setSelectedServiceName(preferred);
      await Promise.all([
        loadRules(appId, preferred),
        loadAvailableAgents(appId, preferred),
      ]);
    } catch (e) {
      showToast(`Failed to load services: ${(e as ApiError).message}`, 'error');
      setServices([]);
      setSelectedServiceName('');
      setAvailableAgents([]);
      setRules([]);
      setSelectedRule(null);
    } finally {
      setServicesLoading(false);
    }
  }, [loadAvailableAgents, loadRules, showToast]);

  const loadApps = useCallback(async () => {
    setAppsLoading(true);
    try {
      const appList = await apiClient.getApps();
      setApps(appList);
      if (appList.length === 0) {
        setSelectedAppId('');
        setServices([]);
        setSelectedServiceName('');
        setRules([]);
        setSelectedRule(null);
        return;
      }

      const preferredAppId = initialAppIdRef.current && appList.some(item => item.id === initialAppIdRef.current)
        ? initialAppIdRef.current
        : appList[0]!.id;
      const preferredServiceName = initialServiceNameRef.current || undefined;

      initialAppIdRef.current = '';
      initialServiceNameRef.current = '';

      setSelectedAppId(preferredAppId);
      await loadServices(preferredAppId, preferredServiceName);
    } catch (e) {
      showToast(`Failed to load apps: ${(e as ApiError).message}`, 'error');
    } finally {
      setAppsLoading(false);
    }
  }, [loadServices, showToast]);

  useEffect(() => {
    loadApps();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const currentAppId = searchParams.get('app_id') || '';
    const currentServiceName = searchParams.get('service_name') || '';
    if (currentAppId === selectedAppId && currentServiceName === selectedServiceName) {
      return;
    }

    const next = new URLSearchParams(searchParams);
    if (selectedAppId) next.set('app_id', selectedAppId);
    else next.delete('app_id');

    if (selectedServiceName) next.set('service_name', selectedServiceName);
    else next.delete('service_name');

    setSearchParams(next, { replace: true });
  }, [searchParams, selectedAppId, selectedServiceName, setSearchParams]);

  useEffect(() => {
    if (selectedRule?.rule_id) {
      void Promise.all([
        loadTargets(selectedRule.rule_id),
        loadRuntimeSnapshot(selectedRule.rule_id),
      ]);
      return;
    }
    setTargets([]);
    setRuntimeSnapshot(null);
  }, [loadRuntimeSnapshot, loadTargets, selectedRule?.rule_id, selectedRule?.updated_at_millis]);

  const appOptions = useMemo<SelectOption[]>(() => apps.map(app => ({
    value: app.id,
    label: app.name || app.id,
    description: `${app.service_count ?? 0} services`,
  })), [apps]);

  const serviceOptions = useMemo<SelectOption[]>(() => services.map(service => ({
    value: service.service_name,
    label: service.service_name,
    description: `${service.instance_count} instances`,
  })), [services]);

  const filteredRules = useMemo(() => {
    const query = search.trim().toLowerCase();
    return rules.filter(rule => {
      if (stateFilter !== 'all' && rule.desired_state !== stateFilter) return false;
      if (typeFilter !== 'all' && rule.instrument_type !== typeFilter) return false;
      if (!query) return true;
      return [
        rule.name,
        rule.description,
        rule.class_name,
        rule.method_name,
        rule.span_name,
        rule.instrument_type,
      ].some(field => (field || '').toLowerCase().includes(query));
    });
  }, [rules, search, stateFilter, typeFilter]);

  const counts = useMemo(() => ({
    total: rules.length,
    active: rules.filter(rule => rule.desired_state === 'active').length,
    paused: rules.filter(rule => rule.desired_state === 'paused').length,
    deleted: rules.filter(rule => rule.desired_state === 'deleted').length,
  }), [rules]);

  const selectedApp = useMemo(
    () => apps.find(app => app.id === selectedAppId) || null,
    [apps, selectedAppId],
  );

  const selectedService = useMemo(
    () => services.find(service => service.service_name === selectedServiceName) || null,
    [services, selectedServiceName],
  );

  const handleAppChange = useCallback(async (appId: string) => {
    if (!appId) return;
    setSelectedAppId(appId);
    setSelectedServiceName('');
    setSelectedRule(null);
    await loadServices(appId);
  }, [loadServices]);

  const handleServiceChange = useCallback(async (serviceName: string) => {
    if (!serviceName) return;
    setSelectedServiceName(serviceName);
    setSelectedRule(null);
    await Promise.all([
      loadRules(selectedAppId, serviceName),
      loadAvailableAgents(selectedAppId, serviceName),
    ]);
  }, [loadAvailableAgents, loadRules, selectedAppId]);

  const handleRefresh = useCallback(async () => {
    if (!selectedAppId) {
      await loadApps();
      return;
    }
    await Promise.all([
      loadServices(selectedAppId, selectedServiceName || undefined),
      selectedRule?.rule_id ? loadTargets(selectedRule.rule_id) : Promise.resolve(),
      selectedRule?.rule_id ? loadRuntimeSnapshot(selectedRule.rule_id) : Promise.resolve(),
    ]);
  }, [loadApps, loadRuntimeSnapshot, loadServices, loadTargets, selectedAppId, selectedRule?.rule_id, selectedServiceName]);

  const openCreateRule = () => {
    setEditingRule(null);
    setEditorOpen(true);
  };

  const openEditRule = (rule: InstrumentationRule) => {
    setEditingRule(rule);
    setEditorOpen(true);
  };

  const handleEditorSubmit = useCallback(async (payload: InstrumentationRuleEditorPayload) => {
    setSubmitting(true);
    try {
      const savedRule = editingRule
        ? await apiClient.updateInstrumentationRule(editingRule.rule_id, payload as never)
        : await apiClient.createInstrumentationRule(payload as never);

      showToast(editingRule ? 'Instrumentation rule updated' : 'Instrumentation rule created', 'success');
      setEditorOpen(false);
      setEditingRule(null);
      await loadRules(savedRule.app_id, savedRule.service_name);
      setSelectedRule(savedRule);
      await loadTargets(savedRule.rule_id);
    } catch (e) {
      showToast(`Failed to save rule: ${(e as ApiError).message}`, 'error');
    } finally {
      setSubmitting(false);
    }
  }, [editingRule, loadRules, loadTargets, showToast]);

  const handlePauseRule = useCallback(async (rule: InstrumentationRule) => {
    const ok = await confirm({
      title: 'Pause Instrumentation Rule',
      message: `Pause rule "${rule.name || summarizeRule(rule)}" and dispatch remove tasks to current targets?`,
      confirmText: 'Pause',
    });
    if (!ok) return;

    try {
      const updated = await apiClient.pauseInstrumentationRule(rule.rule_id);
      showToast('Instrumentation rule paused', 'success');
      await loadRules(updated.app_id, updated.service_name);
      setSelectedRule(updated);
      await Promise.all([
        loadTargets(updated.rule_id),
        loadRuntimeSnapshot(updated.rule_id),
      ]);
    } catch (e) {
      showToast(`Failed to pause rule: ${(e as ApiError).message}`, 'error');
    }
  }, [confirm, loadRules, showToast]);

  const handleResumeRule = useCallback(async (rule: InstrumentationRule) => {
    try {
      const updated = await apiClient.resumeInstrumentationRule(rule.rule_id);
      showToast('Instrumentation rule resumed', 'success');
      await loadRules(updated.app_id, updated.service_name);
      setSelectedRule(updated);
    } catch (e) {
      showToast(`Failed to resume rule: ${(e as ApiError).message}`, 'error');
    }
  }, [loadRules, showToast]);

  const handleDeleteRule = useCallback(async (rule: InstrumentationRule) => {
    const ok = await confirm({
      title: 'Delete Instrumentation Rule',
      message: `Delete rule "${rule.name || summarizeRule(rule)}"? Existing targets will receive remove tasks and the rule will stay visible as deleted history.`,
      confirmText: 'Delete',
      variant: 'danger',
    });
    if (!ok) return;

    try {
      const updated = await apiClient.deleteInstrumentationRule(rule.rule_id);
      showToast('Instrumentation rule deleted', 'success');
      await loadRules(updated.app_id, updated.service_name);
      setSelectedRule(updated);
    } catch (e) {
      showToast(`Failed to delete rule: ${(e as ApiError).message}`, 'error');
    }
  }, [confirm, loadRules, showToast]);

  return (
    <div className="flex flex-col h-full">
      <div className="flex-shrink-0 flex items-center gap-4 pb-2">
        <div>
          <h2 className="text-base font-bold text-gray-800 whitespace-nowrap">Instrumentation</h2>
          <p className="text-[11px] text-gray-400 mt-0.5">Rule-centric dynamic instrumentation workbench</p>
        </div>

        <div className="w-px h-8 bg-gray-200 flex-shrink-0" />

        <SearchableSelect
          options={appOptions}
          value={selectedAppId}
          onChange={handleAppChange}
          placeholder="Select App"
          searchKeys={['label', 'description']}
          loading={appsLoading}
          className="w-48"
        />

        <SearchableSelect
          options={serviceOptions}
          value={selectedServiceName}
          onChange={handleServiceChange}
          placeholder="Select Service"
          searchKeys={['label', 'description']}
          loading={servicesLoading}
          disabled={!selectedAppId || serviceOptions.length === 0}
          className="w-56"
        />

        <div className="flex-1" />

        <div className="hidden xl:flex items-center gap-2 text-[11px] text-gray-500">
          <span className="px-2 py-1 rounded-full bg-white border border-gray-200">{counts.total} rules</span>
          <span className="px-2 py-1 rounded-full bg-green-50 text-green-700 border border-green-100">{counts.active} active</span>
          <span className="px-2 py-1 rounded-full bg-amber-50 text-amber-700 border border-amber-100">{counts.paused} paused</span>
          <span className="px-2 py-1 rounded-full bg-gray-100 text-gray-600 border border-gray-200">{counts.deleted} deleted</span>
        </div>

        <button
          onClick={handleRefresh}
          className="w-8 h-8 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition"
          title="Refresh"
        >
          <i className={`fas fa-sync-alt text-xs ${(appsLoading || servicesLoading || rulesLoading || targetsLoading) ? 'fa-spin' : ''}`} />
        </button>

        <button
          onClick={openCreateRule}
          disabled={!selectedAppId || !selectedServiceName}
          className="px-4 py-2 rounded-lg bg-primary-600 text-white text-sm font-medium hover:bg-primary-700 disabled:opacity-50 disabled:cursor-not-allowed transition flex items-center gap-2"
        >
          <i className="fas fa-plus text-xs" />
          <span>New Rule</span>
        </button>
      </div>

      <div className="flex-1 flex gap-2.5 min-h-0">
        <div className="w-[360px] flex-shrink-0 flex flex-col bg-white border border-gray-200/80 rounded-xl overflow-hidden">
          <div className="p-3 border-b border-gray-100 space-y-3">
            <div className="relative">
              <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-300 text-[10px]" />
              <input
                type="text"
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder="Search by rule, class, method..."
                className="w-full pl-8 pr-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500/30 focus:border-blue-400 text-xs bg-gray-50/50 placeholder:text-gray-300 transition"
              />
            </div>

            <div className="flex flex-wrap gap-2">
              {RULE_STATE_FILTERS.map(item => {
                const active = stateFilter === item.value;
                return (
                  <button
                    key={item.value}
                    onClick={() => setStateFilter(item.value)}
                    className={`px-2.5 py-1 rounded-full text-[11px] font-medium border transition ${active ? 'bg-blue-50 text-blue-700 border-blue-200' : 'bg-white text-gray-500 border-gray-200 hover:bg-gray-50'}`}
                  >
                    {item.label}
                  </button>
                );
              })}
            </div>

            <div className="flex flex-wrap gap-2">
              {RULE_TYPE_FILTERS.map(item => {
                const active = typeFilter === item.value;
                return (
                  <button
                    key={item.value}
                    onClick={() => setTypeFilter(item.value)}
                    className={`px-2.5 py-1 rounded-full text-[11px] font-medium border transition ${active ? 'bg-primary-50 text-primary-700 border-primary-200' : 'bg-white text-gray-500 border-gray-200 hover:bg-gray-50'}`}
                  >
                    {item.label}
                  </button>
                );
              })}
            </div>
          </div>

          <div className="flex-1 overflow-y-auto">
            {!selectedAppId ? (
              <EmptyState
                size="md"
                icon="fas fa-cube"
                iconBg="bg-blue-50"
                iconColor="text-blue-300"
                title="No Application Selected"
                description="请选择应用后再查看动态增强规则。"
                className="px-6"
              />
            ) : !selectedServiceName ? (
              <EmptyState
                size="md"
                icon="fas fa-sitemap"
                iconBg="bg-amber-50"
                iconColor="text-amber-300"
                title="No Service Selected"
                description="当前应用下还没有可用服务，或请先选择一个服务。"
                className="px-6"
              />
            ) : rulesLoading ? (
              <div className="p-3 space-y-2">
                {Array.from({ length: 6 }).map((_, i) => (
                  <div key={i} className="rounded-xl border border-gray-100 p-3 space-y-2">
                    <div className="h-3 skeleton-shimmer rounded w-2/3" />
                    <div className="h-2.5 skeleton-shimmer rounded w-1/2" />
                    <div className="h-2 skeleton-shimmer rounded w-full" />
                  </div>
                ))}
              </div>
            ) : filteredRules.length === 0 ? (
              <EmptyState
                size="md"
                icon="fas fa-wave-square"
                iconBg="bg-purple-50"
                iconColor="text-purple-300"
                title="No Rules Found"
                description={search || stateFilter !== 'all' || typeFilter !== 'all' ? '调整筛选条件后重试。' : '当前服务还没有动态增强规则。'}
                action={
                  !search && stateFilter === 'all' && typeFilter === 'all' ? (
                    <button
                      onClick={openCreateRule}
                      className="px-4 py-2 rounded-lg bg-primary-600 text-white text-sm font-medium hover:bg-primary-700 transition"
                    >
                      Create first rule
                    </button>
                  ) : undefined
                }
                className="px-6"
              />
            ) : (
              <div className="p-2.5 space-y-2">
                {filteredRules.map(rule => {
                  const isSelected = selectedRule?.rule_id === rule.rule_id;
                  return (
                    <button
                      key={rule.rule_id}
                      onClick={() => setSelectedRule(rule)}
                      className={`w-full text-left rounded-xl border px-3 py-3 transition ${isSelected ? 'border-blue-300 bg-blue-50/70 shadow-sm' : 'border-gray-200 hover:border-gray-300 hover:bg-gray-50/70'}`}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2 flex-wrap">
                            <span className="text-sm font-semibold text-gray-800 truncate max-w-[200px]">{rule.name || summarizeRule(rule)}</span>
                            <span className={`px-2 py-0.5 rounded-full text-[10px] font-semibold ring-1 ${ruleStatusClass(rule.summary.status)}`}>
                              {rule.summary.status}
                            </span>
                            <span className="px-2 py-0.5 rounded-full text-[10px] font-medium bg-white text-gray-600 ring-1 ring-gray-200">
                              {rule.instrument_type}
                            </span>
                          </div>
                          <p className="mt-1 text-xs text-gray-500 truncate">{summarizeRule(rule)}</p>
                          <div className="mt-2 flex items-center gap-3 text-[11px] text-gray-400 flex-wrap">
                            <span>{rule.desired_state}</span>
                            <span>{rule.summary.applied_targets}/{rule.summary.total_targets} applied</span>
                            <span>{formatRelativeTime(rule.updated_at_millis)}</span>
                          </div>
                        </div>
                        <div className="flex-shrink-0 w-9 h-9 rounded-lg bg-white/80 border border-gray-200 flex items-center justify-center text-gray-400">
                          <i className="fas fa-wave-square text-sm" />
                        </div>
                      </div>
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        </div>

        <div className="flex-1 min-w-0 flex flex-col bg-white border border-gray-200/80 rounded-xl overflow-hidden">
          {selectedRule ? (
            <>
              <div className="border-b border-gray-100 px-5 py-4">
                <div className="flex items-start justify-between gap-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <h3 className="text-lg font-bold text-gray-800 truncate">{selectedRule.name || summarizeRule(selectedRule)}</h3>
                      <span className={`px-2.5 py-1 rounded-full text-[11px] font-semibold ring-1 ${ruleStatusClass(selectedRule.summary.status)}`}>
                        {selectedRule.summary.status}
                      </span>
                      <span className="px-2.5 py-1 rounded-full text-[11px] font-medium bg-gray-100 text-gray-600 ring-1 ring-gray-200">
                        {selectedRule.desired_state}
                      </span>
                    </div>
                    <p className="mt-1 text-sm text-gray-500 break-all">{summarizeRule(selectedRule)}</p>
                    <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-gray-400">
                      <span>App: {selectedApp?.name || selectedRule.app_id}</span>
                      <span>Service: {selectedRule.service_name}</span>
                      <span>Scope: {selectedRule.scope_type}</span>
                      <span>Updated: {formatTimestamp(selectedRule.updated_at_millis)}</span>
                    </div>
                  </div>

                  <div className="flex items-center gap-2 flex-wrap justify-end">
                    <button
                      onClick={() => void loadRuntimeSnapshot(selectedRule.rule_id, true)}
                      disabled={runtimeSnapshotRefreshing}
                      className="px-3 py-2 rounded-lg border border-blue-200 bg-blue-50 text-blue-700 text-sm font-medium hover:bg-blue-100 disabled:opacity-50 disabled:cursor-not-allowed transition"
                    >
                      <i className={`fas fa-satellite-dish mr-2 text-xs ${runtimeSnapshotRefreshing ? 'fa-spin' : ''}`} />Refresh Snapshot
                    </button>

                    {selectedRule.desired_state === 'active' ? (
                      <button
                        onClick={() => handlePauseRule(selectedRule)}
                        className="px-3 py-2 rounded-lg border border-amber-200 bg-amber-50 text-amber-700 text-sm font-medium hover:bg-amber-100 transition"
                      >
                        <i className="fas fa-pause mr-2 text-xs" />Pause
                      </button>
                    ) : selectedRule.desired_state !== 'deleted' ? (
                      <button
                        onClick={() => handleResumeRule(selectedRule)}
                        className="px-3 py-2 rounded-lg border border-green-200 bg-green-50 text-green-700 text-sm font-medium hover:bg-green-100 transition"
                      >
                        <i className="fas fa-play mr-2 text-xs" />Resume
                      </button>
                    ) : null}

                    {selectedRule.desired_state !== 'deleted' && (
                      <button
                        onClick={() => openEditRule(selectedRule)}
                        className="px-3 py-2 rounded-lg border border-gray-200 bg-white text-gray-700 text-sm font-medium hover:bg-gray-50 transition"
                      >
                        <i className="fas fa-pen mr-2 text-xs" />Edit
                      </button>
                    )}

                    <button
                      onClick={() => handleDeleteRule(selectedRule)}
                      disabled={selectedRule.desired_state === 'deleted'}
                      className="px-3 py-2 rounded-lg border border-red-200 bg-red-50 text-red-700 text-sm font-medium hover:bg-red-100 disabled:opacity-50 disabled:cursor-not-allowed transition"
                    >
                      <i className="fas fa-trash-alt mr-2 text-xs" />Delete
                    </button>
                  </div>
                </div>
              </div>

              <div className="flex-1 overflow-y-auto p-5 space-y-5">
                {/* ===== Health Summary Bar ===== */}
                <HealthSummaryBar rule={selectedRule} runtimeSnapshot={runtimeSnapshot} />

                {/* ===== Detail Tabs ===== */}
                <DetailTabs
                  rule={selectedRule}
                  selectedApp={selectedApp}
                  targets={targets}
                  targetsLoading={targetsLoading}
                  runtimeSnapshot={runtimeSnapshot}
                  runtimeSnapshotLoading={runtimeSnapshotLoading}
                />
              </div>
            </>
          ) : (
            <div className="flex-1 flex items-center justify-center p-8">
              <EmptyState
                size="lg"
                icon="fas fa-wave-square"
                iconBg="bg-primary-50"
                iconColor="text-primary-300"
                title="Select an Instrumentation Rule"
                description={selectedService ? `当前服务 ${selectedService.service_name} 的规则会显示在这里，可查看配置、最近操作与目标实例状态。` : '先选择应用和服务，然后创建或选择一个动态增强规则。'}
                action={selectedAppId && selectedServiceName ? (
                  <button
                    onClick={openCreateRule}
                    className="px-4 py-2 rounded-lg bg-primary-600 text-white text-sm font-medium hover:bg-primary-700 transition"
                  >
                    Create Rule
                  </button>
                ) : undefined}
              />
            </div>
          )}
        </div>
      </div>

      <InstrumentationRuleEditor
        isOpen={editorOpen}
        mode={editingRule ? 'edit' : 'create'}
        appId={selectedAppId}
        serviceName={selectedServiceName}
        availableAgents={availableAgents}
        submitting={submitting}
        rule={editingRule}
        onClose={() => {
          if (submitting) return;
          setEditorOpen(false);
          setEditingRule(null);
        }}
        onSubmit={handleEditorSubmit}
      />
    </div>
  );
}
