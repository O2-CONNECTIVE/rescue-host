// Package guestaccount は一時Guestのdisabled準備と清掃順序だけを扱う。
// native producer・永続journal・SSH有効化は未配線である。
package guestaccount

import "errors"

var (
	ErrUnconfigured = errors.New("一時Guestの生成元は未設定です")
	ErrDenied       = errors.New("一時Guestの操作を拒否しました")
	ErrBusy         = errors.New("一時Guestの別操作が未終了です")
	ErrUnknown      = errors.New("一時Guestの結果が未確定です")
	ErrManual       = errors.New("一時Guestの手動照合が必要です")
	ErrCleanup      = errors.New("一時Guestの清掃は未完了です")
)

type State string

const (
	Unconfigured State = "unconfigured"
	Planned      State = "planned"
	Preparing    State = "preparing"
	Prepared     State = "prepared-disabled"
	Cleaning     State = "cleanup-required"
	Unknown      State = "unknown"
	Quarantined  State = "quarantined"
	Closed       State = "closed"
)

// ProtectedInputs は将来の保護されたproducer専用。任意UI値から構築できない。
type ProtectedInputs struct {
	sessionEpoch, intentID                  [32]byte
	generation                              uint64
	machineSID, operatorSID, serviceUserSID string
	policy                                  limitedPolicy
}

// Plan は純粋な検証結果であり、OS保護や実権限の成立を証明しない。
type Plan struct {
	binding binding
	policy  limitedPolicy
	valid   bool
}

type binding struct {
	epoch, intent                         [32]byte
	policy                                [32]byte
	generation                            uint64
	machine, operator, service, name, sid string
}

type limitedPolicy struct {
	known                           bool
	groups, privileges, logonRights []string
}

type stage uint8

const (
	reserved stage = iota + 1
	createAttempt
	ownedSID
	preparedDisabled
	deleteAttempt
	deleteComplete
)

type record struct {
	binding binding
	stage   stage
}

// Status は診断情報のみ。認可・削除用receiptとして利用できない。
type Status struct {
	State                      State
	Name, SID                  string
	CleanupRequired, Uncertain bool
	// admission閉鎖は実authority失効の完了を意味しない。
	AdmissionClosed, RevocationConfirmed bool
}
