import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

export interface FieldProps {
  label: string;
  htmlFor?: string;
  hint?: string;
  children: ReactNode;
  className?: string;
}

export function Field({ label, htmlFor, hint, children, className }: FieldProps) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <label htmlFor={htmlFor} className="block text-xs font-medium text-ink-soft">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs leading-relaxed text-ink-muted">{hint}</p>}
    </div>
  );
}
