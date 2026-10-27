import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Browser } from 'playwright-core';
import { inspectTextLayout } from './text-layout.ts';
export function registerTextSpacingTests(browser: Browser, url: string): void {
  void test('文字の内部clipを文書幅正常のまま合格にしない', async () => {
    const page = await browser.newPage({ viewport: { width: 320, height: 900 } });
    try {
      await page.goto(url);
      await page.getByRole('button', { name: '診断情報', exact: true }).click();
      assert.deepEqual(await inspectTextLayout(page), { missing: [], clipped: [], horizontal: [] });
      await page.locator('#operation-help').evaluate(node => { node.style.maxHeight = '1px'; node.style.overflow = 'hidden'; });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      assert.ok((await inspectTextLayout(page)).clipped.includes('#operation-help'));
    } finally { await page.close(); }
  });
}
