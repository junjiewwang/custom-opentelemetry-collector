import { cn } from '@/lib/utils';

export interface TabItem {
  id: string;
  label: string;
}

export interface TabsProps {
  items: TabItem[];
  value: string;
  onChange: (id: string) => void;
}

export function Tabs({ items, value, onChange }: TabsProps) {
  return (
    <div role="tablist" aria-label="Tenant settings sections" className="flex gap-1 border-b border-line">
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          role="tab"
          aria-selected={value === item.id}
          onClick={() => onChange(item.id)}
          className={cn(
            '-mb-px border-b-2 px-3 py-2.5 text-sm font-medium transition-colors',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500',
            value === item.id
              ? 'border-primary-600 text-primary-700'
              : 'border-transparent text-ink-muted hover:text-ink',
          )}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}
