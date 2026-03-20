/**
 * TraceComparePage — Trace 比较页面
 *
 * 路由: /traces/compare?traceA={id}&traceB={id}
 *
 * 功能：
 * - 从 URL 参数获取 traceA 和 traceB 的 ID
 * - 调用 apiClient.getTrace() 获取两个 Trace 数据
 * - 加载完成后渲染 TraceDiffView
 * - 如果只有一个 ID，提示用户输入第二个
 * - 手动输入 Trace ID 的输入框（两个），支持粘贴
 */

import { useState, useEffect, useCallback } from 'react';
import { useSearchParams, Link } from 'react-router-dom';
import { apiClient } from '@/api/client';
import type { JaegerTrace } from '@/types/trace';
import TraceDiffView from '@/components/TraceDiffView';
import EmptyState from '@/components/EmptyState';

export default function TraceComparePage() {
  const [searchParams, setSearchParams] = useSearchParams();

  // Input fields
  const [inputA, setInputA] = useState(searchParams.get('traceA') ?? '');
  const [inputB, setInputB] = useState(searchParams.get('traceB') ?? '');

  // Loaded trace data
  const [traceA, setTraceA] = useState<JaegerTrace | null>(null);
  const [traceB, setTraceB] = useState<JaegerTrace | null>(null);

  // Loading / error state
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // ========================================================================
  // Fetch traces
  // ========================================================================

  const fetchTrace = useCallback(async (traceID: string): Promise<JaegerTrace | null> => {
    const resp = await apiClient.getTrace(traceID);
    const data = resp.data;
    if (!data || data.length === 0) {
      throw new Error(`Trace "${traceID}" not found`);
    }
    return data[0]!;
  }, []);

  const doCompare = useCallback(async (idA: string, idB: string) => {
    if (!idA.trim() || !idB.trim()) {
      setError('Please enter both Trace IDs');
      return;
    }

    if (idA.trim() === idB.trim()) {
      setError('Please enter two different Trace IDs');
      return;
    }

    setLoading(true);
    setError('');
    setTraceA(null);
    setTraceB(null);

    try {
      const [tA, tB] = await Promise.all([
        fetchTrace(idA.trim()),
        fetchTrace(idB.trim()),
      ]);
      setTraceA(tA);
      setTraceB(tB);

      // 同步 URL
      setSearchParams({ traceA: idA.trim(), traceB: idB.trim() }, { replace: true });
    } catch (err: unknown) {
      const apiErr = err as { message?: string };
      setError(apiErr.message ?? 'Failed to fetch traces');
    } finally {
      setLoading(false);
    }
  }, [fetchTrace, setSearchParams]);

  // ========================================================================
  // 自动加载（URL 参数带有两个 ID 时）
  // ========================================================================

  useEffect(() => {
    const idA = searchParams.get('traceA');
    const idB = searchParams.get('traceB');
    if (idA && idB) {
      setInputA(idA);
      setInputB(idB);
      doCompare(idA, idB);
    }
    // 只在组件挂载时执行一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ========================================================================
  // Handlers
  // ========================================================================

  const handleCompare = () => {
    doCompare(inputA, inputB);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleCompare();
    }
  };

  const handleSwap = () => {
    setInputA(inputB);
    setInputB(inputA);
    if (traceA && traceB) {
      setTraceA(traceB);
      setTraceB(traceA);
      setSearchParams({ traceA: inputB, traceB: inputA }, { replace: true });
    }
  };

  // ========================================================================
  // Render
  // ========================================================================

  return (
    <div className="fade-in">
      {/* Page Header */}
      <div className="mb-6">
        <div className="flex items-center gap-3 mb-2">
          <Link
            to="/traces"
            className="text-gray-400 hover:text-gray-600 transition"
            title="Back to Traces"
          >
            <i className="fas fa-arrow-left" />
          </Link>
          <h2 className="text-2xl font-bold text-gray-800 flex items-center gap-3">
            <i className="fas fa-columns text-primary-600" />
            Compare Traces
          </h2>
        </div>
        <p className="text-gray-500 mt-1">
          Compare two traces side-by-side to identify performance differences
        </p>
      </div>

      {/* Input Panel */}
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6 mb-6">
        <div className="flex items-end gap-3">
          {/* Trace A Input */}
          <div className="flex-1">
            <label className="block text-sm font-medium text-gray-700 mb-1">
              <span className="inline-flex items-center gap-1">
                <span className="w-2.5 h-2.5 bg-blue-400 rounded-sm" />
                Trace A
              </span>
            </label>
            <input
              type="text"
              value={inputA}
              onChange={(e) => setInputA(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Enter Trace ID (e.g. abc123...)"
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm font-mono focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            />
          </div>

          {/* Swap Button */}
          <button
            onClick={handleSwap}
            className="p-2 text-gray-400 hover:text-gray-600 hover:bg-gray-100 rounded-lg transition mb-0.5"
            title="Swap A ↔ B"
          >
            <i className="fas fa-exchange-alt" />
          </button>

          {/* Trace B Input */}
          <div className="flex-1">
            <label className="block text-sm font-medium text-gray-700 mb-1">
              <span className="inline-flex items-center gap-1">
                <span className="w-2.5 h-2.5 bg-purple-400 rounded-sm" />
                Trace B
              </span>
            </label>
            <input
              type="text"
              value={inputB}
              onChange={(e) => setInputB(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Enter Trace ID (e.g. def456...)"
              className="w-full px-3 py-2 border border-gray-200 rounded-lg text-sm font-mono focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            />
          </div>

          {/* Compare Button */}
          <button
            onClick={handleCompare}
            disabled={loading || !inputA.trim() || !inputB.trim()}
            className="px-6 py-2 bg-primary-600 text-white rounded-lg hover:bg-primary-700 transition flex items-center gap-2 disabled:opacity-50 flex-shrink-0"
          >
            {loading ? (
              <i className="fas fa-spinner fa-spin" />
            ) : (
              <i className="fas fa-columns" />
            )}
            <span>{loading ? 'Loading...' : 'Compare'}</span>
          </button>
        </div>

        {/* Error */}
        {error && (
          <div className="mt-4 px-4 py-2 bg-red-50 text-red-600 rounded-lg text-sm">
            <i className="fas fa-exclamation-circle mr-2" />
            {error}
          </div>
        )}
      </div>

      {/* Loading State */}
      {loading && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-12 text-center">
          <i className="fas fa-spinner fa-spin text-3xl text-primary-400 mb-4" />
          <p className="text-gray-500">Loading traces for comparison...</p>
        </div>
      )}

      {/* Diff View */}
      {!loading && traceA && traceB && (
        <TraceDiffView
          traceA={traceA}
          traceB={traceB}
          onClose={() => {
            setTraceA(null);
            setTraceB(null);
          }}
        />
      )}

      {/* Empty State — no IDs provided */}
      {!loading && !traceA && !traceB && !error && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200">
          <EmptyState
            icon="fas fa-columns"
            iconColor="text-primary-300"
            iconBg="bg-primary-50"
            title="Compare Two Traces"
            description={
              <span>
                Enter two Trace IDs above and click <strong>Compare</strong> to view a side-by-side diff.
                You can also navigate here from the Traces list by copying Trace IDs.
              </span>
            }
            size="lg"
          />
        </div>
      )}
    </div>
  );
}
