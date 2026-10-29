const names = ['navigation-allowed', 'navigation-denied', 'load-started', 'load-finished'] as const;
type Name = typeof names[number];
type Snapshot = { schema: 1; phase: string; dropped: number; events: Name[] };

function snapshot(value: unknown, phase: string): value is Snapshot {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return v.schema === 1 && v.phase === phase && v.dropped === 0 && Array.isArray(v.events)
    && v.events.length <= 32 && v.events.every(x => typeof x === 'string' && names.includes(x as Name));
}

export function classifyNavigationSnapshots(before: unknown, after: unknown) {
  const valid = snapshot(before, 'before') && snapshot(after, 'after')
    && before.events.length <= after.events.length && before.events.every((x, i) => after.events[i] === x);
  const classify = (name: Name) => !valid ? 'unknown' : before.events.includes(name) ? 'before-request'
    : after.events.includes(name) ? 'between-snapshots' : 'unobserved';
  return { diagnostic: valid ? 'complete' : 'incomplete', navigationAllowed: classify('navigation-allowed'),
    navigationDenied: classify('navigation-denied'), loadStarted: classify('load-started'), loadFinished: classify('load-finished') };
}
