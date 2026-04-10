import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';

interface ShellChromeState {
  /** 当前是否需要隐藏全局浮动控件（如桌面端侧边栏折叠按钮） */
  floatingUIHidden: boolean;
  /** 获取一个浮动控件隐藏锁；返回的 cleanup 用于释放 */
  acquireFloatingUILock: () => () => void;
}

const ShellChromeContext = createContext<ShellChromeState | null>(null);

export function ShellChromeProvider({ children }: { children: ReactNode }) {
  const [floatingUILockCount, setFloatingUILockCount] = useState(0);

  const acquireFloatingUILock = useCallback(() => {
    setFloatingUILockCount(count => count + 1);

    let released = false;
    return () => {
      if (released) return;
      released = true;
      setFloatingUILockCount(count => Math.max(0, count - 1));
    };
  }, []);

  const value = useMemo<ShellChromeState>(() => ({
    floatingUIHidden: floatingUILockCount > 0,
    acquireFloatingUILock,
  }), [floatingUILockCount, acquireFloatingUILock]);

  return (
    <ShellChromeContext.Provider value={value}>
      {children}
    </ShellChromeContext.Provider>
  );
}

export function useShellChrome(): ShellChromeState {
  const ctx = useContext(ShellChromeContext);
  if (!ctx) throw new Error('useShellChrome must be used within ShellChromeProvider');
  return ctx;
}
