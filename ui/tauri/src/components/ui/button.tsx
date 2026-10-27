// Adapted from shadcn/ui; provenance and full license: third-party/README.md.
import type { ComponentProps } from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';

const buttonVariants = cva('button', {
  variants: { variant: { default: '', danger: 'button--danger', quiet: 'button--quiet' } },
  defaultVariants: { variant: 'default' }
});
export function Button({ className, variant, asChild = false, type = 'button', ...props }:
  ComponentProps<'button'> & VariantProps<typeof buttonVariants> & { asChild?: boolean }) {
  const Component = asChild ? Slot : 'button';
  return <Component data-slot="button" type={type} className={buttonVariants({ variant, className })} {...props} />;
}
