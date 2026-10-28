export type HistoryResult = 'confirmed' | 'unconfirmed' | 'expired' | 'failed';
export interface HistoryEntry {
  readonly displayName: string;
  readonly occurredAt: number;
  readonly result: HistoryResult;
}

// Memory-only prototype: no timer or durable deletion/security guarantee.
// Clock rollback keeps future-dated entries until their recorded time + 30 days.
export function createMemoryHistory({ now }: { now: () => number }) {
  type Item = { entry: HistoryEntry };
  let items: Item[] = [];
  let entries: readonly HistoryEntry[] = Object.freeze([]);
  const owners = new WeakMap<HistoryEntry, Item>();
  const listeners = new Set<() => void>();
  function timestamp() {
    const value = now();
    if (!Number.isSafeInteger(value) || value < 0) throw new RangeError('Invalid mock history clock');
    return value;
  }
  function publish() {
    entries = Object.freeze(items.map(item => item.entry));
    for (const listener of [...listeners]) listener();
  }
  function pruneAt(timestamp: number) {
    const next = items.filter(item => timestamp - item.entry.occurredAt < 30 * 24 * 60 * 60 * 1_000);
    if (next.length === items.length) return;
    items = next;
    publish();
  }
  function prune() { pruneAt(timestamp()); }
  return {
    mode: 'memory_mock' as const,
    entries: () => entries,
    record: (displayName: string, result: HistoryResult) => {
      const occurredAt = timestamp();
      pruneAt(occurredAt);
      const item = { entry: Object.freeze({ displayName, occurredAt, result }) };
      owners.set(item.entry, item);
      items.push(item);
      publish();
      return (next: HistoryResult) => {
        if (!items.includes(item)) return;
        prune();
        if (!items.includes(item) || item.entry.result === next) return;
        item.entry = Object.freeze({ displayName, occurredAt: item.entry.occurredAt, result: next });
        owners.set(item.entry, item);
        publish();
      };
    },
    remove(entry: HistoryEntry) {
      const item = owners.get(entry);
      if (!item || !items.includes(item)) return;
      items = items.filter(candidate => candidate !== item);
      publish();
    },
    clear() {
      if (items.length === 0) return;
      items = [];
      publish();
    },
    prune,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    }
  };
}
