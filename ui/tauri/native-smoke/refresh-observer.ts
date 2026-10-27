import { setTimeout as delay } from 'node:timers/promises';
import type { Driver } from './webdriver.ts';
type Page = Pick<Driver, 'execute'>;
export async function armRefresh(driver: Page): Promise<void> {
  const armed = await driver.execute(`const refresh = document.querySelector('#refresh');
    if (!refresh || refresh.getAttribute('aria-busy') !== 'false') return false;
    window.__rescueKeyboardRefresh?.observer.disconnect();
    const state = {started:false, completed:false};
    const observer = new MutationObserver(records => {
      // Record an intermediate busy state even if both mutations share a callback.
      if (refresh.getAttribute('aria-busy') === 'true' || records.some(r=>r.oldValue === 'true')) state.started = true;
      if (state.started && refresh.getAttribute('aria-busy') === 'false') state.completed = true;
    });
    observer.observe(refresh, {attributes:true, attributeFilter:['aria-busy'], attributeOldValue:true});
    window.__rescueKeyboardRefresh = {state, observer};
    return true;`);
  if (armed !== true) throw new Error('NATIVE_REFRESH_ARM_FAILED');
}
export async function disarmRefresh(driver: Page): Promise<void> {
  await driver.execute('window.__rescueKeyboardRefresh?.observer.disconnect(); delete window.__rescueKeyboardRefresh; return true;');
}
export async function waitForRefresh(driver: Page): Promise<{ started: boolean; completed: boolean }> {
  const until = Date.now() + 3000;
  let started = false;
  let completed = false;
  do {
    const value = await driver.execute(`const observation = window.__rescueKeyboardRefresh?.state;
      return {started:observation?.started === true, completed:observation?.completed === true,
        settled:document.querySelector('#diagnostic')?.textContent === 'SHELL_UNWIRED'
          && document.querySelector('#refresh')?.getAttribute('aria-busy') === 'false'};`);
    if (value !== null && typeof value === 'object' && 'started' in value && 'completed' in value && 'settled' in value) {
      started = value.started === true;
      completed = value.completed === true && value.settled === true;
      if (started && completed) break;
    }
    await delay(100);
  } while (Date.now() < until);
  return { started, completed };
}
