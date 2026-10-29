package serviceready

import "context"

// SplitForInspectionは接続済みの同Endpointから検査専用leaseを取得し、
// duplexが閉じられる前に保持してから既存Splitでreader/writerを分離する。
// leaseは認証証拠ではなく、0x120080の照合専用複製だけを借用する。
// errorと併返する部分leaseもcallerが全queryの実return後にCloseする。
// Endpointは同leaseの実回収までCloseEndpointのackを返さない。
// 元workerの固定threadで一度だけ呼び、既存leaseを再利用しない。
func (e *Endpoint) SplitForInspection(ctx context.Context) (*SoleInput, *InspectionLease, error) {
	lease, err := e.NewInspectionLease(ctx)
	if err != nil {
		return nil, lease, err
	}
	input, err := e.Split(ctx)
	return input, lease, err
}
