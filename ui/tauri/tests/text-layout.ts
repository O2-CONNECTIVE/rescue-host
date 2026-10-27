import type { Page } from 'playwright-core';
export interface TextLayout { missing: string[]; clipped: string[]; horizontal: string[] }
// Limited to the current shell with diagnostics expanded; not a general a11y audit.
export async function inspectTextLayout(page: Page): Promise<TextLayout> {
  return page.evaluate(() => {
    const missing = new Set<string>();
    const clipped = new Set<string>();
    const horizontal = new Set<string>();
    const tolerance = 1;
    if (document.documentElement.scrollWidth > innerWidth + tolerance) horizontal.add('document');
    const targets = ['header', '#status-title', '#status-description', '#permission-title', '.shell-permissions', '.shell-actions', '#operation-help', '[data-slot="collapsible-trigger"]', '[data-slot="collapsible-content"]', 'footer'];
    for (const selector of targets) {
      const target = document.querySelector(selector);
      if (!target) { missing.add(selector); continue; }
      const walker = document.createTreeWalker(target, NodeFilter.SHOW_TEXT);
      let text: Node | null;
      let textFound = false;
      while ((text = walker.nextNode())) {
        if (!text.textContent?.trim()) continue;
        textFound = true;
        const range = document.createRange();
        range.selectNodeContents(text);
        const rectangles = [...range.getClientRects()].filter(rect => rect.width > 0 && rect.height > 0);
        if (!rectangles.length) missing.add(selector);
        for (let ancestor = text.parentElement; ancestor; ancestor = ancestor.parentElement) {
          const style = getComputedStyle(ancestor);
          if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse' || Number(style.opacity) === 0) missing.add(selector);
          const box = ancestor.getBoundingClientRect();
          const left = box.left + ancestor.clientLeft;
          const top = box.top + ancestor.clientTop;
          for (const rect of rectangles) {
            if (rect.left < -tolerance || rect.right > innerWidth + tolerance) horizontal.add(selector);
            if (['hidden', 'clip'].includes(style.overflowX) && (rect.left < left - tolerance || rect.right > left + ancestor.clientWidth + tolerance)) clipped.add(selector);
            if (['hidden', 'clip'].includes(style.overflowY) && (rect.top < top - tolerance || rect.bottom > top + ancestor.clientHeight + tolerance)) clipped.add(selector);
          }
          if (['auto', 'scroll'].includes(style.overflowX) && ancestor.scrollWidth > ancestor.clientWidth + tolerance) horizontal.add(selector);
        }
      }
      if (!textFound) missing.add(selector);
    }
    return { missing: [...missing], clipped: [...clipped], horizontal: [...horizontal] };
  });
}
