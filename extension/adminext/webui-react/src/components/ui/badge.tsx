import { cva, type VariantProps } from 'class-variance-authority';
import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/utils';

const badgeVariants = cva(
  'inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-tight',
  {
    variants: {
      tone: {
        neutral: 'border-line bg-canvas text-ink-muted',
        blue: 'border-primary-200 bg-primary-50 text-primary-700',
        amber: 'border-warning-100 bg-warning-50 text-warning-700',
        green: 'border-success-100 bg-success-50 text-success-600',
        red: 'border-danger-100 bg-danger-50 text-danger-600',
      },
    },
    defaultVariants: { tone: 'neutral' },
  },
);

export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, tone, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ tone, className }))} {...props} />;
}
