import { forwardRef, type InputHTMLAttributes } from 'react';
import { cn } from '@/lib/utils';

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  error?: string;
  mono?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, error, mono, id, ...props }, ref) => (
    <div className="w-full">
      <input
        id={id}
        ref={ref}
        aria-invalid={!!error || undefined}
        aria-describedby={error && id ? `${id}-error` : undefined}
        className={cn(
          'flex h-10 w-full rounded-ui border border-line bg-surface px-3 text-sm text-ink placeholder:text-ink-faint',
          'focus-visible:border-primary-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-100',
          'disabled:cursor-not-allowed disabled:bg-canvas disabled:text-ink-faint',
          mono && 'font-mono text-[13px]',
          error && 'border-danger-500 focus-visible:ring-danger-100',
          className,
        )}
        {...props}
      />
      {error && (
        <p id={id ? `${id}-error` : undefined} className="mt-1.5 text-xs text-danger-600" role="alert">
          {error}
        </p>
      )}
    </div>
  ),
);
Input.displayName = 'Input';
