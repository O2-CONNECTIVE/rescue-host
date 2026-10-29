export function inspectBaselineDocument(value: unknown) {
  const input = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
  const field = (name: string, expected: string, nullable = false) => {
    if (!Object.hasOwn(input, name)) return 'unknown';
    const actual = input[name];
    if (nullable && actual === null) return 'mismatch';
    if (typeof actual !== 'string') return 'unknown';
    return actual === expected ? 'match' : 'mismatch';
  };
  const result = { href: field('href', 'about:blank'), uri: field('uri', 'about:blank'), origin: field('origin', 'null'),
    marker: field('marker', 'Wry creation contrast', true), tauri: field('tauri', 'undefined') };
  const markerDetail = input.markerPresent === false && input.marker === null ? 'absent'
    : input.markerPresent === true && result.marker === 'match' ? 'match'
      : input.markerPresent === true && typeof input.marker === 'string' ? 'text-mismatch' : 'unknown';
  const readyState = input.readyState === 'loading' || input.readyState === 'interactive' || input.readyState === 'complete'
    ? input.readyState : 'unknown';
  return { ...result, accepted: Object.values(result).every(x => x === 'match'), markerDetail, readyState };
}
