# 登録後に具体 SoleInput を移す private factory

`prepareNativeSoleInputProfileBatch` は同交換の具体 `*serviceready.SoleInput` を保持する未配線の private source である。
prepare は Move、thread/token 照会、候補 process の取得、pipe 作成を行わない。
同じ Batch / Collector / issuer / canonical Owner を Service が登録した後、固定 worker の Start admission と ServiceScope 取得を経て Move する。
登録自体の exact identity と Service への組込みは Service owner の責務であり、この constructor の戻り値だけで登録済みとはしない。

production は `moveNativeSoleInput` から具体 `SoleInput.Move` method へ固定接続する。
公開の InputMover、任意 callback、成功 bool、新しい peer-query lease API は追加しない。
`nativeBatchOps.moveSoleInput` は package 内の typed memory kernel seam であり、公開 factory の引数ではない。
成功 SoleInput を偽造する公開 test constructor や unsafe は使わない。
zero SoleInput を production method に渡す試験では実 method の拒否を確認する。

Move の非零実 return は、error・handle 値・alias の判定より先に同じ Batch の `input` へ記録する。
通常 error、取消、issuer 停止の後も戻った read は同じ Batch が回収し、候補照会や helper 起動へ進まない。
非 return 中の Close は constructor の completion を代替せず、有限期限で未確認を返す。
panic で return がない場合は元の具体 source を Batch に残し、未知の Endpoint/read 参照を回収済みへ変えない。
事前 Close は Move しない。未移動 read は Endpoint / Service owner が回収する。

Move が正常に戻り admission が継続している場合だけ、同じ実 input を既存 `endpointCandidateLease` に単回で bind する。
bind 前の候補照会、再 bind、Close 後の bind は拒否する。
provider はこの同じ lease から候補 process/token を取得し、取得後は保持参照だけを再検査する。
Batch が親 read を閉じた後、古い handle 番号を再 query しない既存契約を維持する。
raw input の従来入口は所有移譲済み read の契約として残すが、新 typed factory は raw input との併用を拒否する。

pseudo handle や helper/Service 借用 token との alias は保持したまま未知にし、close しない。
profile/report の返した handle も raw spec のゼロ値でなく、実所有 input・借用 token・互いの handle と照合する。
所有者が重複する場合は起動も二重 close も行わず、同じ owner に未確認参照を残す。
Move の `serviceready.ErrFatal` は普通の拒否へ縮小せず、Batch に sticky な fatal として保持する。
既知 fatal 後に native query/cleanup を再開せず、Service worker の既存退役契約へ返す。

typed Move は所有移動であり、operator 認証や同じ Service/profile の証明ではない。
same Service/profile の実照合、独立 AuthenticationId を含む正規 policy producer、公開 native factory の登録は未完成である。
欠けた policy を候補から補完せず、既存 provider の厳密な必須値検査を維持する。
製品 gate は閉鎖したままで、OS/ACL/account/資格情報/実 SSH/P2 の操作や成功を今回の memory 試験へ含めない。

## 今回の検証記録

base は `929d516173dbbf772ba224d5dde500f6e51f1501`。
以下は同じ private memory seam で振る舞いの Red を確認してから修正した。

| 試験 | Red の観測 | Green の境界 |
| --- | --- | --- |
| `TestSoleInputBatchMovesOnlyAfterPreparedOwnerRegistration` | Start が Move を呼ばず I/O を開始 | prepare はゼロ、登録後 Start は単回 Move |
| `TestSoleInputBatchBindsMovedInputToExistingCandidateLease` | 候補が移動済み input に未結合で Start 拒否 | 同じ実 input を既存 lease へ bind |
| `TestSoleInputBatchRetainsInvalidOrBorrowedReturnWithoutClosing` | pseudo/借用 token の return を close して所有記録を消失 | 未知を保持し close/query しない |
| `TestSoleInputBatchFatalMoveRetiresWithoutNativeCleanup` | fatal を拒否へ縮小して owner を再利用 | 非零 return を保持後、fatal を sticky 伝播 |
| `TestSoleInputBatchRejectsProfileReportAliasesBeforeHelperStart` | moved input の alias が launch/cleanup へ到達。alias と併返の constructor fatal も消失 | 実 input と全 wire handle を照合し、fatal 分類も維持 |

部分 error return、取消・停止後の return、close 不明、非 return 中の Close、Move panic、事前 Close、zero の実 SoleInput method、欠けた policy/raw 併用、lease の再 bind/Close 後 bind は追加の回帰検査である。
`go test -race -json ./internal/operatorauth -count=1` は top-level 167 / subtest 145 が pass、fail/skip はともにゼロ、package exit 0。
`GOOS=windows GOARCH=amd64 go test -c ./internal/operatorauth` は exit 0 で、生成物は実行していない。
gofmt と `git diff --check` は成功。
通常 JSON・vet・helper を含む範囲、Windows arm64 compile、固定 head の独立 review/CI は統合 owner の検証として別に記録する。
