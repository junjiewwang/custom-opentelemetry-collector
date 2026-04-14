import { useEffect, useMemo, useState } from 'react';
import Modal from '@/components/Modal';
import type { Instance } from '@/types/api';
import type {
  CreateInstrumentationRuleRequest,
  InstrumentationRule,
  InstrumentationInstrumentType,
  InstrumentationScopeType,
  UpdateInstrumentationRuleRequest,
} from '@/types/instrumentation';

export type InstrumentationRuleEditorPayload = CreateInstrumentationRuleRequest | UpdateInstrumentationRuleRequest;

interface InstrumentationRuleEditorProps {
  isOpen: boolean;
  mode: 'create' | 'edit';
  appId: string;
  serviceName: string;
  availableAgents: Instance[];
  submitting?: boolean;
  rule?: InstrumentationRule | null;
  onClose: () => void;
  onSubmit: (payload: InstrumentationRuleEditorPayload) => Promise<void> | void;
}

interface RuleFormState {
  name: string;
  description: string;
  scope_type: InstrumentationScopeType;
  target_agent_ids: string[];
  class_name: string;
  method_name: string;
  parameter_types: string;
  method_descriptor: string;
  instrument_type: InstrumentationInstrumentType;
  span_name: string;
  capture_args: string;
  capture_return: string;
  capture_max_length: number;
  force: boolean;
}

const DEFAULT_FORM: RuleFormState = {
  name: '',
  description: '',
  scope_type: 'service',
  target_agent_ids: [],
  class_name: '',
  method_name: '',
  parameter_types: '',
  method_descriptor: '',
  instrument_type: 'trace',
  span_name: '',
  capture_args: '',
  capture_return: '',
  capture_max_length: 256,
  force: false,
};

function buildInitialState(rule?: InstrumentationRule | null): RuleFormState {
  if (!rule) return DEFAULT_FORM;
  return {
    name: rule.name || '',
    description: rule.description || '',
    scope_type: rule.scope_type,
    target_agent_ids: rule.target_agent_ids || [],
    class_name: rule.class_name,
    method_name: rule.method_name,
    parameter_types: rule.parameter_types || '',
    method_descriptor: rule.method_descriptor || '',
    instrument_type: rule.instrument_type,
    span_name: rule.span_name || '',
    capture_args: rule.capture_args || '',
    capture_return: rule.capture_return || '',
    capture_max_length: rule.capture_max_length || 256,
    force: Boolean(rule.force),
  };
}

export default function InstrumentationRuleEditor({
  isOpen,
  mode,
  appId,
  serviceName,
  availableAgents,
  submitting = false,
  rule,
  onClose,
  onSubmit,
}: InstrumentationRuleEditorProps) {
  const [form, setForm] = useState<RuleFormState>(DEFAULT_FORM);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!isOpen) return;
    setForm(buildInitialState(rule));
    setError('');
  }, [isOpen, rule]);

  const sortedAgents = useMemo(
    () => [...availableAgents].sort((a, b) => a.agent_id.localeCompare(b.agent_id)),
    [availableAgents],
  );

  const updateField = <K extends keyof RuleFormState>(key: K, value: RuleFormState[K]) => {
    setForm(prev => ({ ...prev, [key]: value }));
  };

  const toggleAgent = (agentId: string) => {
    setForm(prev => ({
      ...prev,
      target_agent_ids: prev.target_agent_ids.includes(agentId)
        ? prev.target_agent_ids.filter(item => item !== agentId)
        : [...prev.target_agent_ids, agentId],
    }));
  };

  const handleSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const className = form.class_name.trim();
    const methodName = form.method_name.trim();

    if (!appId || !serviceName) {
      setError('请先选择应用和服务。');
      return;
    }
    if (!className) {
      setError('请输入类名。');
      return;
    }
    if (!methodName) {
      setError('请输入方法名。');
      return;
    }
    if (form.scope_type === 'instance' && form.target_agent_ids.length === 0) {
      setError('实例级规则至少需要选择一个目标实例。');
      return;
    }

    setError('');

    const basePayload = {
      name: form.name.trim(),
      description: form.description.trim(),
      scope_type: form.scope_type,
      target_agent_ids: form.scope_type === 'instance' ? form.target_agent_ids : undefined,
      class_name: className,
      method_name: methodName,
      parameter_types: form.parameter_types.trim() || undefined,
      method_descriptor: form.method_descriptor.trim() || undefined,
      instrument_type: form.instrument_type,
      span_name: form.span_name.trim() || undefined,
      capture_args: form.capture_args.trim() || undefined,
      capture_return: form.capture_return.trim() || undefined,
      capture_max_length: Number.isFinite(form.capture_max_length) ? form.capture_max_length : 256,
      force: form.force,
    };

    if (mode === 'create') {
      await onSubmit({
        ...basePayload,
        app_id: appId,
        service_name: serviceName,
        created_by: 'webui',
      });
      return;
    }

    await onSubmit({
      ...basePayload,
      updated_by: 'webui',
    });
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      size="lg"
      title={mode === 'create' ? 'Create Instrumentation Rule' : 'Edit Instrumentation Rule'}
    >
      <form onSubmit={handleSubmit} className="p-6 space-y-5">
        <div className="rounded-xl border border-blue-100 bg-blue-50/70 px-4 py-3 text-sm text-blue-700 flex items-start gap-3">
          <i className="fas fa-sparkles mt-0.5 text-blue-500" />
          <div>
            <div className="font-semibold">当前上下文</div>
            <div className="mt-1 text-blue-600/90 break-all">
              <span className="font-medium">App:</span> {appId || '-'}
              <span className="mx-2 text-blue-300">/</span>
              <span className="font-medium">Service:</span> {serviceName || '-'}
            </div>
          </div>
        </div>

        {error && (
          <div className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-600">
            {error}
          </div>
        )}

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Rule Name</span>
            <input
              type="text"
              value={form.name}
              onChange={e => updateField('name', e.target.value)}
              placeholder="留空则自动生成"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Instrument Type</span>
            <select
              value={form.instrument_type}
              onChange={e => updateField('instrument_type', e.target.value as InstrumentationInstrumentType)}
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none bg-white"
            >
              <option value="trace">trace</option>
              <option value="metric">metric</option>
              <option value="log">log</option>
            </select>
          </label>

          <label className="block md:col-span-2">
            <span className="text-xs font-semibold text-gray-600">Description</span>
            <textarea
              value={form.description}
              onChange={e => updateField('description', e.target.value)}
              rows={3}
              placeholder="说明此规则的目的、适用场景和验证方式"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Class Name *</span>
            <input
              type="text"
              value={form.class_name}
              onChange={e => updateField('class_name', e.target.value)}
              placeholder="com.example.OrderService"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Method Name *</span>
            <input
              type="text"
              value={form.method_name}
              onChange={e => updateField('method_name', e.target.value)}
              placeholder="createOrder"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Parameter Types</span>
            <input
              type="text"
              value={form.parameter_types}
              onChange={e => updateField('parameter_types', e.target.value)}
              placeholder="java.lang.String,int"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Method Descriptor</span>
            <input
              type="text"
              value={form.method_descriptor}
              onChange={e => updateField('method_descriptor', e.target.value)}
              placeholder="(Ljava/lang/String;)V"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Scope</span>
            <select
              value={form.scope_type}
              onChange={e => updateField('scope_type', e.target.value as InstrumentationScopeType)}
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none bg-white"
            >
              <option value="service">service</option>
              <option value="instance">instance</option>
            </select>
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Span Name</span>
            <input
              type="text"
              value={form.span_name}
              onChange={e => updateField('span_name', e.target.value)}
              placeholder="可选，自定义 span 名称"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Capture Args</span>
            <input
              type="text"
              value={form.capture_args}
              onChange={e => updateField('capture_args', e.target.value)}
              placeholder="arg0,arg1 或自定义表达式"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Capture Return</span>
            <input
              type="text"
              value={form.capture_return}
              onChange={e => updateField('capture_return', e.target.value)}
              placeholder="return 或自定义表达式"
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>

          <label className="block">
            <span className="text-xs font-semibold text-gray-600">Capture Max Length</span>
            <input
              type="number"
              min={1}
              value={form.capture_max_length}
              onChange={e => updateField('capture_max_length', Number(e.target.value) || 256)}
              className="mt-1 w-full rounded-lg border border-gray-200 px-3 py-2 text-sm focus:border-blue-400 focus:ring-2 focus:ring-blue-500/20 outline-none"
            />
          </label>
        </div>

        <label className="flex items-center gap-2 text-sm text-gray-700">
          <input
            type="checkbox"
            checked={form.force}
            onChange={e => updateField('force', e.target.checked)}
            className="rounded border-gray-300 text-blue-600 focus:ring-blue-500/20"
          />
          Force apply even when previous instrumentation exists
        </label>

        {form.scope_type === 'instance' && (
          <div className="rounded-xl border border-gray-200 bg-gray-50/70 p-4">
            <div className="flex items-center justify-between gap-3 mb-3">
              <div>
                <h4 className="text-sm font-semibold text-gray-700">Target Instances</h4>
                <p className="text-xs text-gray-500 mt-1">选择当前服务下需要下发该规则的实例。</p>
              </div>
              <span className="text-xs font-medium text-gray-400">{form.target_agent_ids.length} selected</span>
            </div>
            {sortedAgents.length === 0 ? (
              <div className="rounded-lg border border-dashed border-gray-300 bg-white px-4 py-6 text-sm text-gray-500 text-center">
                当前服务下没有可选实例，请先确认实例已注册。
              </div>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-2 max-h-60 overflow-y-auto">
                {sortedAgents.map(agent => {
                  const checked = form.target_agent_ids.includes(agent.agent_id);
                  const online = agent.status?.state === 'online';
                  return (
                    <label
                      key={agent.agent_id}
                      className={`rounded-lg border px-3 py-2.5 flex items-start gap-3 cursor-pointer transition ${
                        checked ? 'border-blue-300 bg-blue-50' : 'border-gray-200 bg-white hover:border-gray-300'
                      }`}
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggleAgent(agent.agent_id)}
                        className="mt-0.5 rounded border-gray-300 text-blue-600 focus:ring-blue-500/20"
                      />
                      <div className="min-w-0">
                        <div className="text-sm font-medium text-gray-700 break-all">{agent.agent_id}</div>
                        <div className="mt-1 text-xs text-gray-500 flex flex-wrap gap-x-2 gap-y-1">
                          <span>{agent.hostname || '-'}</span>
                          <span>{agent.ip || '-'}</span>
                          <span className={online ? 'text-green-600' : 'text-gray-400'}>{online ? 'online' : agent.status?.state || 'unknown'}</span>
                        </div>
                      </div>
                    </label>
                  );
                })}
              </div>
            )}
          </div>
        )}

        <div className="flex justify-end gap-3 pt-2 border-t border-gray-100">
          <button
            type="button"
            onClick={onClose}
            className="px-4 py-2 rounded-lg border border-gray-200 text-sm text-gray-600 hover:bg-gray-50 transition"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={submitting}
            className="px-4 py-2 rounded-lg bg-blue-600 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-60 disabled:cursor-not-allowed transition"
          >
            {submitting ? 'Saving...' : mode === 'create' ? 'Create Rule' : 'Save Changes'}
          </button>
        </div>
      </form>
    </Modal>
  );
}
