type Start = { slot: number; kind: 'start'; decision: 'allowed' | 'denied'; cancel: 'call-ok' | 'call-error'; uri: 'fixture' | 'blank' | 'other' };
type Finish = { slot: number; kind: 'complete'; success: 'success' | 'failure' };
type PairEvent = Start | Finish;
type Snapshot = { pairs: PairEvent[] };
function snapshot(value: unknown, phase: string): value is Snapshot {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  if (v.schema !== 1 || v.phase !== phase || v.dropped !== 0 || !Array.isArray(v.pairs) || v.pairs.length > 32) return false;
  return v.pairs.every((item: unknown) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return false;
    const p = item as Record<string, unknown>;
    if (!Number.isInteger(p.slot) || (p.slot as number) < 1 || (p.slot as number) > 16) return false;
    return p.kind === 'start' ? ['allowed', 'denied'].includes(p.decision as string)
      && ['call-ok', 'call-error'].includes(p.cancel as string) && ['fixture', 'blank', 'other'].includes(p.uri as string)
      : p.kind === 'complete' && ['success', 'failure'].includes(p.success as string);
  });
}
export function classifyNavigationPairs(before: unknown, after: unknown) {
  const invalid = () => ({ diagnostic: 'incomplete', snapshotState: 'invalid', pairs: [] });
  if (!snapshot(before, 'before') || !snapshot(after, 'after') || before.pairs.length > after.pairs.length
    || !before.pairs.every((item, index) => JSON.stringify(item) === JSON.stringify(after.pairs[index]))) return invalid();
  const pairs: { slot: number; starts: { decision: string; cancel: string; uri: string; observed: string }[];
    completion: { success: string; observed: string } | null }[] = [];
  for (const [index, event] of after.pairs.entries()) {
    let entry = pairs.find(p => p.slot === event.slot);
    if (!entry) { entry = { slot: event.slot, starts: [], completion: null }; pairs.push(entry); }
    const observed = index < before.pairs.length ? 'before-request' : 'by-after-snapshot';
    if (event.kind === 'start' && !entry.completion) entry.starts.push({ decision: event.decision, cancel: event.cancel, uri: event.uri, observed });
    else if (event.kind === 'complete' && entry.starts.length > 0 && !entry.completion) entry.completion = { success: event.success, observed };
    else return invalid();
  }
  return { diagnostic: 'incomplete', snapshotState: 'observed', pairs };
}
