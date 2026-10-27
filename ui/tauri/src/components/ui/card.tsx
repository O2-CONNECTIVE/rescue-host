// Adapted from shadcn/ui; only the used Card root is retained.
import type { ComponentProps } from 'react';
export function Card({ className = '', ...props }: ComponentProps<'div'>) {
  return <div data-slot="card" className={`shell-panel ${className}`} {...props} />;
}
