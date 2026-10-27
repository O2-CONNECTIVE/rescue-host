// Adapted from shadcn/ui; Radix is the only primitive backend.
import type { ComponentProps } from 'react';
import * as Primitive from '@radix-ui/react-collapsible';
export function Collapsible(props: ComponentProps<typeof Primitive.Root>) {
  return <Primitive.Root data-slot="collapsible" {...props} />;
}
export function CollapsibleTrigger(props: ComponentProps<typeof Primitive.Trigger>) {
  return <Primitive.Trigger data-slot="collapsible-trigger" {...props} />;
}
export function CollapsibleContent(props: ComponentProps<typeof Primitive.Content>) {
  return <Primitive.Content data-slot="collapsible-content" {...props} />;
}
