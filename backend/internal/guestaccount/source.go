package guestaccount

import "context"

// sealはpackage外のcallerがboolやcallbackからreceiptを発行することを防ぐ。
// 将来のnative producerには実対象を保持したままのSID原子的操作が必要。
type seal struct{ marker byte }
type journalReceipt struct {
	seal   *seal
	record record
}
type accountReceipt struct {
	seal    *seal
	binding binding
	kind    accountKind
}
type sessionReceipt struct {
	seal    *seal
	binding binding
	kind    sessionKind
}
type accountKind uint8

const (
	created accountKind = iota + 1
	disabled
	deleted
)

type sessionKind uint8

const (
	revoked sessionKind = iota + 1
	quiescent
)

type presence uint8

const (
	exact presence = iota + 1
	absent
	foreign
)

type accountSnapshot struct {
	seal     *seal
	binding  binding
	presence presence
	// completeは直接/間接effective groups、privileges、logon rights、
	// 保護対象write accessを全取得したproducerだけが設定する。nil一覧は拒否。
	complete, disabled, administrator, protectedWrites bool
	groups, privileges, logonRights                    []string
}

// journalSourceは保護installation scopeの排他所有下で使う。reserveは既存
// intentを上書きせず原子的に登録する。saveは同一不変bindingと同一SID
// （最初のSID確定だけを除く）を条件に、許可済み単調stage遷移だけをcommit
// する。同じrecordへの再保存は冪等で、terminalの回帰/他intentの上書きは
// 拒否する。特に削除済みaccountの終端commit再試行にこの条件が必要である。
// receiptはdurable commit確認後だけ返し、loadも同じ排他scopeの確定記録
// を読む。privateなWindows候補はjournal*.goにあるが、正規root producerと
// 製品への配線は未設定である。memoryは製品へ接続しない。
type journalSource interface {
	reserve(context.Context, record) (journalReceipt, error)
	save(context.Context, record) (journalReceipt, error)
	load(context.Context) (journalReceipt, error)
}
type accountInspector interface {
	inspect(context.Context, binding) (accountSnapshot, error)
}
type accountSource interface {
	accountInspector
	createDisabled(context.Context, binding) (accountReceipt, error)
	disable(context.Context, binding) (accountReceipt, error)
	deleteExact(context.Context, binding) (accountReceipt, error)
}
type sessionSource interface {
	revoke(context.Context, binding) (sessionReceipt, error)
	quiesce(context.Context, binding) (sessionReceipt, error)
}

// previousSessionSourceは旧epochの実対象を回収する専用producer。
// 新しい空sessionの成功を旧sessionの清掃証拠として代用しない。
type previousSessionSource interface {
	previousBinding() binding
	revokePrevious(context.Context, binding) (sessionReceipt, error)
	quiescePrevious(context.Context, binding) (sessionReceipt, error)
}
type sources struct {
	journal                               journalSource
	accounts                              accountSource
	session                               sessionSource
	previous                              previousSessionSource
	journalSeal, accountSeal, sessionSeal *seal
}

// knownFailureは副作用なし/既知失敗をproducerが区別した場合だけ使う。
// context cancel等の任意errorは既知失敗に変換しない。
type knownFailure struct{}

func (knownFailure) Error() string { return "既知の拒否" }
