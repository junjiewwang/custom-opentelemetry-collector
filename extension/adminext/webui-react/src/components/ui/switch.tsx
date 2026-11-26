import { cn } from '@/lib/utils';

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  description?: string;
  disabled?: boolean;
}

/** Accessible switch with a visible focus ring and a full-click target. */
export function Switch({ checked, onChange, label, description, disabled }: SwitchProps) {
  return (
    <div className="flex items-start gap-3">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cn(
          'relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2',
          'disabled:cursor-not-allowed disabled:opacity-50',
          checked ? 'bg-primary-600' : 'bg-line-strong',
        )}
      >
        <span
          className={cn(
            'absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white shadow-sm transition-transform',
            checked && 'translate-x-4',
          )}
        />
      </button>
      <div className="min-w-0">
        <div className={cn('text-sm font-medium', disabled ? 'text-ink-faint' : 'text-ink')}>{label}</div>
        {description && <div className="mt-0.5 text-xs leading-relaxed text-ink-muted">{description}</div>}
      </div>
    </div>
  );
}
