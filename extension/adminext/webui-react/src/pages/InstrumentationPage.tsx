import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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

function isOfflineTarget(target: InstrumentationRuleTargetStatus): boolean {
  return OFFLINE_STATES.includes(target.state);
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

/** Target Status 分组表格：在线实例正常展示，离线/过期实例折叠展示 */
function TargetStatusTable({ targets }: { targets: InstrumentationRuleTargetStatus[] }) {
  const [offlineExpanded, setOfflineExpanded] = useState(false);

  const onlineTargets = useMemo(() => targets.filter(t => !isOfflineTarget(t)), [targets]);
  const offlineTargets = useMemo(() => targets.filter(t => isOfflineTarget(t)), [targets]);

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
          {onlineTargets.map(target => (
            <TargetRow key={`${target.rule_id}:${target.agent_id}`} target={target} />
          ))}

          {offlineTargets.length > 0 && (
            <>
              <tr className="bg-slate-50/60">
                <td colSpan={6} className="px-4 py-2">
                  <button
                    onClick={() => setOfflineExpanded(!offlineExpanded)}
                    className="flex items-center gap-2 text-xs font-medium text-slate-500 hover:text-slate-700 transition-colors w-full"
                  >
                    <i className={`fas fa-chevron-right text-[10px] transition-transform duration-200 ${offlineExpanded ? 'rotate-90' : ''}`} />
                    <i className="fas fa-plug-circle-xmark text-slate-400" />
                    <span>
                      Offline / Expired Instances ({offlineTargets.length})
                    </span>
                    <span className="ml-auto text-slate-400 font-normal">
                      {offlineExpanded ? 'click to collapse' : 'click to expand'}
                    </span>
                  </button>
                </td>
              </tr>
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
                <div className="grid grid-cols-1 xl:grid-cols-4 gap-3">
                  {[
                    { label: 'Targets', value: selectedRule.summary.total_targets, color: 'bg-gray-50 text-gray-700' },
                    { label: 'Applied', value: selectedRule.summary.applied_targets, color: 'bg-green-50 text-green-700' },
                    { label: 'Pending / Running', value: selectedRule.summary.pending_targets + selectedRule.summary.running_targets, color: 'bg-blue-50 text-blue-700' },
                    { label: 'Failed / Offline / Expired', value: selectedRule.summary.failed_targets + selectedRule.summary.offline_targets + (selectedRule.summary.expired_targets || 0), color: 'bg-red-50 text-red-700' },
                  ].map(card => (
                    <div key={card.label} className="rounded-xl border border-gray-200 bg-white p-4">
                      <p className="text-xs text-gray-500">{card.label}</p>
                      <div className="mt-2 flex items-end justify-between">
                        <span className={`text-2xl font-bold ${card.color.split(' ')[1]}`}>{card.value}</span>
                        <span className={`px-2 py-1 rounded-full text-[11px] font-medium ${card.color}`}>{card.label}</span>
                      </div>
                    </div>
                  ))}
                </div>

                <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
                  <section className="rounded-xl border border-gray-200 bg-white">
                    <div className="px-4 py-3 border-b border-gray-100 flex items-center gap-2">
                      <i className="fas fa-sliders-h text-primary-500 text-sm" />
                      <h4 className="text-sm font-semibold text-gray-800">Rule Configuration</h4>
                    </div>
                    <div className="p-4 grid grid-cols-1 md:grid-cols-2 gap-3 text-sm">
                      <div>
                        <div className="text-xs text-gray-400">Description</div>
                        <div className="mt-1 text-gray-700 break-words">{selectedRule.description || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Instrument Type</div>
                        <div className="mt-1 text-gray-700">{selectedRule.instrument_type}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Parameter Types</div>
                        <div className="mt-1 text-gray-700 break-all">{selectedRule.parameter_types || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Method Descriptor</div>
                        <div className="mt-1 text-gray-700 break-all">{selectedRule.method_descriptor || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Span Name</div>
                        <div className="mt-1 text-gray-700 break-all">{selectedRule.span_name || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Capture Max Length</div>
                        <div className="mt-1 text-gray-700">{selectedRule.capture_max_length || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Capture Args</div>
                        <div className="mt-1 text-gray-700 break-all">{selectedRule.capture_args || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Capture Return</div>
                        <div className="mt-1 text-gray-700 break-all">{selectedRule.capture_return || '-'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Target Agents</div>
                        <div className="mt-1 text-gray-700 break-all">{selectedRule.target_agent_ids?.length ? selectedRule.target_agent_ids.join(', ') : 'service-wide'}</div>
                      </div>
                      <div>
                        <div className="text-xs text-gray-400">Force Apply</div>
                        <div className="mt-1 text-gray-700">{selectedRule.force ? 'true' : 'false'}</div>
                      </div>
                    </div>
                  </section>

                  <section className="rounded-xl border border-gray-200 bg-white">
                    <div className="px-4 py-3 border-b border-gray-100 flex items-center gap-2">
                      <i className="fas fa-history text-blue-500 text-sm" />
                      <h4 className="text-sm font-semibold text-gray-800">Last Operation</h4>
                    </div>
                    <div className="p-4 text-sm">
                      {selectedRule.last_operation ? (
                        <div className="space-y-3">
                          <div className="flex items-center gap-2 flex-wrap">
                            <span className={`px-2.5 py-1 rounded-full text-[11px] font-semibold ring-1 ${ruleStatusClass(selectedRule.last_operation.status)}`}>
                              {selectedRule.last_operation.status}
                            </span>
                            <span className="px-2.5 py-1 rounded-full text-[11px] font-medium bg-gray-100 text-gray-600 ring-1 ring-gray-200">
                              {selectedRule.last_operation.type}
                            </span>
                            <span className="text-xs text-gray-400">{selectedRule.last_operation.operation_id}</span>
                          </div>
                          <div className="grid grid-cols-2 gap-3 text-sm">
                            <div>
                              <div className="text-xs text-gray-400">Started At</div>
                              <div className="mt-1 text-gray-700">{formatTimestamp(selectedRule.last_operation.started_at_millis)}</div>
                            </div>
                            <div>
                              <div className="text-xs text-gray-400">Completed At</div>
                              <div className="mt-1 text-gray-700">{formatTimestamp(selectedRule.last_operation.completed_at_millis)}</div>
                            </div>
                            <div>
                              <div className="text-xs text-gray-400">Applied</div>
                              <div className="mt-1 text-gray-700">{selectedRule.last_operation.applied_targets}</div>
                            </div>
                            <div>
                              <div className="text-xs text-gray-400">Failed</div>
                              <div className="mt-1 text-gray-700">{selectedRule.last_operation.failed_targets}</div>
                            </div>
                            <div>
                              <div className="text-xs text-gray-400">Pending</div>
                              <div className="mt-1 text-gray-700">{selectedRule.last_operation.pending_targets}</div>
                            </div>
                            <div>
                              <div className="text-xs text-gray-400">Offline</div>
                              <div className="mt-1 text-gray-700">{selectedRule.last_operation.offline_targets}</div>
                            </div>
                            <div>
                              <div className="text-xs text-gray-400">Expired</div>
                              <div className="mt-1 text-gray-700">{selectedRule.last_operation.expired_targets || 0}</div>
                            </div>
                          </div>
                        </div>
                      ) : (
                        <EmptyState
                          size="sm"
                          icon="fas fa-history"
                          iconBg="bg-blue-50"
                          iconColor="text-blue-300"
                          title="No operation yet"
                          description="规则创建后会在这里展示最近一次下发动作与聚合状态。"
                        />
                      )}
                    </div>
                  </section>
                </div>

                <section className="rounded-xl border border-gray-200 bg-white overflow-hidden">
                  <div className="px-4 py-3 border-b border-gray-100 flex items-center justify-between gap-3">
                    <div>
                      <h4 className="text-sm font-semibold text-gray-800">Recent Audit</h4>
                      <p className="text-xs text-gray-400 mt-1">查看最近几次手工操作与后台 reconcile 收敛动作。</p>
                    </div>
                    <span className="text-xs text-gray-400">{selectedRule.recent_audits?.length ?? 0} entries</span>
                  </div>

                  {selectedRule.recent_audits?.length ? (
                    <div className="divide-y divide-gray-100">
                      {selectedRule.recent_audits.slice(0, 8).map((audit: InstrumentationRuleAuditEntry) => (
                        <div key={audit.audit_id} className="px-4 py-3 flex items-start justify-between gap-4">
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2 flex-wrap">
                              <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${auditStatusClass(audit.status)}`}>
                                {audit.status}
                              </span>
                              <span className="inline-flex px-2 py-1 rounded-full text-[11px] font-medium bg-gray-100 text-gray-600 ring-1 ring-gray-200">
                                {formatAuditSource(audit.source)}
                              </span>
                              <span className="inline-flex px-2 py-1 rounded-full text-[11px] font-medium bg-blue-50 text-blue-700 ring-1 ring-blue-200">
                                {formatAuditAction(audit.action)}
                              </span>
                              {audit.agent_id ? <span className="text-xs text-gray-400 break-all">agent: {audit.agent_id}</span> : null}
                            </div>
                            <div className="mt-2 text-sm text-gray-600 break-words">{audit.message || '-'}</div>
                            {audit.task_id ? <div className="mt-1 text-xs text-gray-400 break-all">task: {audit.task_id}</div> : null}
                          </div>
                          <div className="text-xs text-gray-400 whitespace-nowrap">{formatRelativeTime(audit.created_at_millis)}</div>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <EmptyState
                      size="sm"
                      icon="fas fa-scroll"
                      iconBg="bg-indigo-50"
                      iconColor="text-indigo-300"
                      title="No audit yet"
                      description="后端收敛动作和规则下发动作会在这里保留最近几次审计记录。"
                    />
                  )}
                </section>

                <section className="rounded-xl border border-gray-200 bg-white overflow-hidden">
                  <div className="px-4 py-3 border-b border-gray-100 flex items-center justify-between gap-3">
                    <div>
                      <h4 className="text-sm font-semibold text-gray-800">Runtime Snapshot</h4>
                      <p className="text-xs text-gray-400 mt-1">查看 JVM 运行时快照、freshness、能力摘要与 drift 诊断。</p>
                    </div>
                    <div className="flex items-center gap-2 text-xs text-gray-400">
                      {runtimeSnapshot?.generated_at_millis ? <span>generated {formatRelativeTime(runtimeSnapshot.generated_at_millis)}</span> : null}
                      <span>{runtimeSnapshot?.targets.length ?? 0} targets</span>
                    </div>
                  </div>

                  {runtimeSnapshotLoading ? (
                    <div className="p-4 space-y-3">
                      <div className="grid grid-cols-1 md:grid-cols-4 gap-3">
                        {Array.from({ length: 4 }).map((_, i) => (
                          <div key={i} className="h-20 skeleton-shimmer rounded-xl" />
                        ))}
                      </div>
                      {Array.from({ length: 3 }).map((_, i) => (
                        <div key={i} className="h-14 skeleton-shimmer rounded-lg" />
                      ))}
                    </div>
                  ) : !runtimeSnapshot ? (
                    <EmptyState
                      size="sm"
                      icon="fas fa-satellite-dish"
                      iconBg="bg-blue-50"
                      iconColor="text-blue-300"
                      title="No runtime snapshot yet"
                      description="当前还没有可展示的 JVM 运行时快照，可点击 Refresh Snapshot 主动查询。"
                      className="px-6"
                    />
                  ) : (
                    <div className="p-4 space-y-4">
                      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-5 gap-3">
                        {[
                          { label: 'Snapshot Ready', value: runtimeSnapshot.summary.snapshot_available_targets, tone: 'bg-blue-50 text-blue-700' },
                          { label: 'Runtime Found', value: runtimeSnapshot.summary.runtime_found_targets, tone: 'bg-indigo-50 text-indigo-700' },
                          { label: 'Effective', value: runtimeSnapshot.summary.effective_targets, tone: 'bg-green-50 text-green-700' },
                          { label: 'Drifted', value: runtimeSnapshot.summary.drifted_targets, tone: 'bg-amber-50 text-amber-700' },
                          { label: 'Stale / Failed', value: runtimeSnapshot.summary.stale_targets + runtimeSnapshot.summary.refresh_failed_targets, tone: 'bg-red-50 text-red-700' },
                        ].map(card => (
                          <div key={card.label} className="rounded-xl border border-gray-200 bg-white p-4">
                            <p className="text-xs text-gray-500">{card.label}</p>
                            <div className="mt-2 flex items-end justify-between gap-2">
                              <span className={`text-2xl font-bold ${card.tone.split(' ')[1]}`}>{card.value}</span>
                              <span className={`px-2 py-1 rounded-full text-[11px] font-medium ${card.tone}`}>{card.label}</span>
                            </div>
                          </div>
                        ))}
                      </div>

                      <div className="overflow-x-auto">
                        <table className="min-w-full divide-y divide-gray-100 text-sm">
                          <thead className="bg-gray-50/80">
                            <tr className="text-left text-xs uppercase tracking-wide text-gray-400">
                              <th className="px-4 py-3 font-medium">Agent</th>
                              <th className="px-4 py-3 font-medium">Controlplane</th>
                              <th className="px-4 py-3 font-medium">Runtime</th>
                              <th className="px-4 py-3 font-medium">Drift</th>
                              <th className="px-4 py-3 font-medium">Freshness</th>
                              <th className="px-4 py-3 font-medium">Diagnostics</th>
                            </tr>
                          </thead>
                          <tbody className="divide-y divide-gray-100 bg-white">
                            {runtimeSnapshot.targets.map((target: InstrumentationRuleRuntimeSnapshotTarget) => (
                              <tr key={`${target.rule_id}:${target.agent_id}`} className="align-top">
                                <td className="px-4 py-3">
                                  <div className="font-medium text-gray-800 break-all">{target.agent_id}</div>
                                  <div className="text-xs text-gray-400 mt-1">{target.hostname || '-'} / {target.ip || '-'}</div>
                                </td>
                                <td className="px-4 py-3">
                                  <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${targetStateClass(target.controlplane_state)}`}>
                                    {target.controlplane_state}
                                  </span>
                                  <div className="text-xs text-gray-400 mt-1">desired: {target.desired_state}</div>
                                  <div className="text-xs text-gray-400 mt-1">task: {target.controlplane_task_status || '-'}</div>
                                </td>
                                <td className="px-4 py-3 text-gray-600">
                                  <div className="flex flex-wrap gap-2">
                                    <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${target.runtime_found ? 'bg-green-50 text-green-700 ring-green-200' : 'bg-gray-100 text-gray-600 ring-gray-200'}`}>
                                      {target.runtime_found ? (target.runtime_status || 'found') : 'not_found'}
                                    </span>
                                    <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${target.is_effective ? 'bg-green-50 text-green-700 ring-green-200' : 'bg-gray-100 text-gray-600 ring-gray-200'}`}>
                                      {target.is_effective ? 'effective' : 'not effective'}
                                    </span>
                                    {target.snapshot_available ? (
                                      <span className="inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 bg-blue-50 text-blue-700 ring-blue-200">
                                        snapshot ready
                                      </span>
                                    ) : (
                                      <span className="inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 bg-gray-100 text-gray-600 ring-gray-200">
                                        no snapshot
                                      </span>
                                    )}
                                  </div>
                                  <div className="mt-2 text-xs text-gray-400">transformers: {target.active_transformer_count ?? 0}</div>
                                </td>
                                <td className="px-4 py-3">
                                  {target.drift_reasons?.length ? (
                                    <div className="flex flex-wrap gap-1.5 max-w-xs">
                                      {target.drift_reasons.map(reason => (
                                        <span key={reason} className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${driftReasonClass(reason)}`}>
                                          {formatDriftReason(reason)}
                                        </span>
                                      ))}
                                    </div>
                                  ) : (
                                    <span className="text-xs text-gray-400">-</span>
                                  )}
                                </td>
                                <td className="px-4 py-3 text-gray-500 whitespace-nowrap">
                                  <div className="flex flex-wrap gap-1.5">
                                    <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${runtimeRefreshStatusClass(target.last_refresh_status)}`}>
                                      {target.last_refresh_status || 'idle'}
                                    </span>
                                    {target.is_stale ? (
                                      <span className="inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 bg-amber-50 text-amber-700 ring-amber-200">stale</span>
                                    ) : null}
                                    {target.dirty ? (
                                      <span className="inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 bg-orange-50 text-orange-700 ring-orange-200">dirty</span>
                                    ) : null}
                                  </div>
                                  <div className="mt-2 text-xs text-gray-400">refreshed: {formatRelativeTime(target.refreshed_at_millis)}</div>
                                  <div className="mt-1 text-xs text-gray-400">expires: {formatRelativeTime(target.expires_at_millis)}</div>
                                </td>
                                <td className="px-4 py-3 text-gray-500 max-w-sm">
                                  <div className="flex flex-wrap gap-1.5 mb-2">
                                    <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${target.instrumentation_available ? 'bg-green-50 text-green-700 ring-green-200' : 'bg-slate-100 text-slate-700 ring-slate-200'}`}>
                                      instr {target.instrumentation_available ? 'ready' : 'unavailable'}
                                    </span>
                                    <span className={`inline-flex px-2 py-1 rounded-full text-[11px] font-semibold ring-1 ${target.enhancement_capability ? 'bg-green-50 text-green-700 ring-green-200' : 'bg-slate-100 text-slate-700 ring-slate-200'}`}>
                                      capability {target.enhancement_capability ? 'ready' : 'missing'}
                                    </span>
                                  </div>
                                  <div className="space-y-1 text-xs leading-5">
                                    {target.instrumentation_source ? <div>source: {target.instrumentation_source}</div> : null}
                                    {target.diagnostic_message ? <div className="break-words">diagnostic: {target.diagnostic_message}</div> : null}
                                    {target.last_error_message ? <div className="break-words text-red-600">error: {target.last_error_message}</div> : null}
                                    {!target.instrumentation_source && !target.diagnostic_message && !target.last_error_message ? <div>-</div> : null}
                                  </div>
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    </div>
                  )}
                </section>

                <section className="rounded-xl border border-gray-200 bg-white overflow-hidden">
                  <div className="px-4 py-3 border-b border-gray-100 flex items-center justify-between gap-3">
                    <div>
                      <h4 className="text-sm font-semibold text-gray-800">Target Status</h4>
                      <p className="text-xs text-gray-400 mt-1">查看每个实例上的任务状态与错误信息。</p>
                    </div>
                    <span className="text-xs text-gray-400">{targets.length} targets</span>
                  </div>

                  {targetsLoading ? (
                    <div className="p-4 space-y-2">
                      {Array.from({ length: 4 }).map((_, i) => (
                        <div key={i} className="h-12 skeleton-shimmer rounded-lg" />
                      ))}
                    </div>
                  ) : targets.length === 0 ? (
                    <EmptyState
                      size="sm"
                      icon="fas fa-server"
                      iconBg="bg-gray-50"
                      iconColor="text-gray-300"
                      title="No targets yet"
                      description="当前规则还没有解析出目标实例，或目标状态尚未生成。"
                      className="px-6"
                    />
                  ) : (
                    <TargetStatusTable targets={targets} />
                  )}
                </section>
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
