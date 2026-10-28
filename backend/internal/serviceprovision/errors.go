package serviceprovision

import "errors"

var (
	ErrLaunchTokenUnconfigured = errors.New("起動用tokenの保護供給が未設定です")
	ErrApprovalUnconfigured    = errors.New("operatorの新規承認入口が未設定です")
)
