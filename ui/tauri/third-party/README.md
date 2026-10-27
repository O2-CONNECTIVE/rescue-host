# Owned shadcn/ui sources

Upstream commit: `d094cd36caf871f2fde3e339b87aa43b9af5a7d0`.
Source: https://github.com/shadcn-ui/ui/tree/d094cd36caf871f2fde3e339b87aa43b9af5a7d0/apps/v4/registry/new-york-v4/ui
MIT copyright and permission notice: [LICENSE.shadcn](LICENSE.shadcn).
Only Button, Card root, and Collapsible are retained in `src/components/ui`.
Adaptations: semantic CSS instead of Tailwind utilities; individual official Radix packages instead of aggregate radix-ui; only used variants/exports; native button type defaults to button; React types imported explicitly.

| Original source | SHA-256 |
| --- | --- |
| button.tsx | `79dd6f75f8136394442202d6b8b922fb269eaad0a5dba579397c9d5b41f893bb` |
| card.tsx | `ef8305b12c3112dab42a4708b41b1ebcc042fa5ab4c20f039da8d45b09ed5059` |
| collapsible.tsx | `c6598e5584f4258b5d9fefadbc5ad6a66888ad58729f4b634a0bb2d9c513188b` |

Runtime npm license texts and applicable notices are assembled from the locked installed packages into dist/THIRD_PARTY_NOTICES.txt. Build fails when a runtime package has no license file. This does not claim the separate Rust distribution inventory is complete.
