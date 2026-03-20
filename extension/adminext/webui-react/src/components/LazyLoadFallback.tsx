/**
 * 懒加载 Fallback 组件
 * 用于 React.lazy + Suspense 的 loading 状态展示
 */

export default function LazyLoadFallback() {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-[200px] gap-3">
      <div className="w-8 h-8 border-4 border-gray-200 border-t-blue-500 rounded-full animate-spin" />
      <span className="text-sm text-gray-400">Loading...</span>
    </div>
  );
}
