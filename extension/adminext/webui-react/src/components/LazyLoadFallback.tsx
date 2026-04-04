/**
 * 懒加载 Fallback 组件
 * 用于 React.lazy + Suspense 的 loading 状态展示
 * 
 * 升级版：三点脉冲动画 + 品牌色渐变，提升视觉质感
 */

export default function LazyLoadFallback() {
  return (
    <div className="flex flex-col items-center justify-center h-full min-h-[200px] gap-4">
      {/* 三点脉冲动画 */}
      <div className="flex items-center gap-1.5">
        <span className="w-2.5 h-2.5 rounded-full bg-blue-400 animate-bounce" style={{ animationDelay: '0ms' }} />
        <span className="w-2.5 h-2.5 rounded-full bg-blue-500 animate-bounce" style={{ animationDelay: '150ms' }} />
        <span className="w-2.5 h-2.5 rounded-full bg-blue-600 animate-bounce" style={{ animationDelay: '300ms' }} />
      </div>
      {/* 骨架占位条 */}
      <div className="flex flex-col items-center gap-2 w-48">
        <div className="h-2 skeleton-shimmer rounded-full w-full" />
        <div className="h-2 skeleton-shimmer rounded-full w-3/4" />
      </div>
      <span className="text-xs text-gray-400 mt-1">Loading...</span>
    </div>
  );
}
