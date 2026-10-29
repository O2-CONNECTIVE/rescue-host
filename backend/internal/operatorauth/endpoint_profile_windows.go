//go:build windows

package operatorauth

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"

// 正規policy発行元とService adapterは未接続。候補とは独立に供給したpolicyと、
// same exchangeの破壊的Moveが返したread所有参照が必要である。
// pure prepareだけを行い、実取得はService登録後の同worker Startに限定する。
func prepareNativeEndpointProfileBatch(s nativeBatchSpec, factory *bootstrapauth.Factory, serviceToken uintptr, owner batchOwner, policy profileProviderPolicy) (*nativeBatch, error) {
	s, err := endpointProfileSpec(s, policy, &nativeEndpointCandidateKernel{})
	if err != nil {
		return nil, err
	}
	return prepareNativeBatch(s, factory, serviceToken, owner)
}
