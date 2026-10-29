// 独立対照の結果だけを判定し、製品合格を返さない。
export function decideBaseline(_collection: unknown, _wry: unknown, _session: unknown) {
  return { diagnostic: 'incomplete', outcome: 'inconclusive', contrastPassed: false, productNativePass: false };
}
