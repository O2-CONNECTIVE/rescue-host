# Native bootstrap の受信

`NativeReceiver` は、保護された service の発信元を認証してから一回だけ起動記録を読み、配置済みの鍵を取得して `runbackend.ApprovedConfig` を返す。
Authority や Session の生成、有効化、listener の作成は行わない。
製品の起動ゲートは閉鎖を維持する。

## 保護された入力と相関値

`NewNativeReceiver` には固定した `bootstrapauth.Profile`、`ReceiverLimits`、最大実行時間、lease 上限、各収容数、startup timeout をすべて明示する。
最大実行時間は 2 時間、lease は 1 分、channels は 32、handshakes と transports は各 64 を超えられず、保護された構成でさらに厳しくできる。
constructor は静的検証と deep copy だけを行い、認証や DLL 呼出し、ファイル読取りを開始しない。
未構成、未知の profile、不足した上限、zero receiver は拒否する。

argv の専用 handle locator と起動相関値は `UntrustedLaunchArgs` として解析する。
locator は pipe の認証にならず、未認証の locator を読み取ったり close したりしない。
`bootstrapauth.Factory.Authenticate` の成功後にだけ、独立した issuer、generation、identity、artifact、nonce、policyRef を package 内の非公開 context に束縛する。
generation は正数、session epoch は 0 を含む既存 control/1 の規則を保つ。

## 一回の受信と鍵取得

`ReadApproved` の最初の呼出しで受信権を消費する。
不正な引数、拒否、取消、部分的な失敗、回収未確認でも再受信できない。
caller は専用 goroutine で `runtime.LockOSThread` を行い、認証から鍵 scope の close まで同じ OS thread を所有する。
library は worker への移動、権限の追加、impersonation の解除、`Goexit` を行わない。

受信処理は次の順で進む。

1. 構成済み factory が current principal、固定 child image、借用 server-read handle、kernel の pipe client PID、固定 service の SCM RUNNING/PID、保持した peer の token、image、creation time を確認する。
2. 成功した handle を `bootstrapio.AdoptNativeReader` に渡し、context に属する Reader が一つの message と LF、続く read の実 EOF を確認する。
3. 非公開 parser が構造と意味を検査し、独立した argv の全相関値、保護された receiver/backend 上限、固定 key reference catalog を照合する。
4. 元の hard deadline を `context.WithDeadline` へそのまま渡して短い期限を作り、期限切れなら鍵取得前に拒否する。
   以降の peer 再検査、実 `protectedkey.BeginNativeScope`、構成済み `protectedkey.Loader.Load`、取得後の再検査はすべてこの短い context を使う。
5. 鍵取得が失敗した場合も peer を再検査する。
6. 同じ thread で鍵 scope を閉じ、peer と Reader の回収を確認した後にだけ設定を返す。

`DecodeRecord(record, fullExpected, limits)` は従来どおり独立した全既知値との純粋な比較 API を保つ。
Native 受信は認証後に同じ非公開 parser を使用し、payload を複製して `Expected` を作らない。
公開 raw decode、verified bool、role 文字列から信頼済み context を作る入口はない。

記録内の公開鍵集合から digest を再計算し、mode と policy digest、grant と identity、lease と各上限を照合する。
絶対 hard deadline は元の値を保持し、相対時間へ再基準化しない。
Native 受信は照合後の期限を早期に適用し、実行開始時の最終期限判定は既存 `runbackend.New` の契約を維持する。
payload が持つ鍵情報は固定 catalog の参照名だけで、秘密鍵の生成や wire への直列化は行わない。

## 回収と fatal の扱い

認証の Peer は read handle を借用する。
認証が成功した時点で receiver が raw handle を所有し、Reader への adopt が成功すると所有権を Reader に移す。
adopt が失敗した場合は認証済み raw handle を専用 close helper で回収し、失敗時は参照を次の `Close` まで保持する。
認証失敗で返った Peer の query handle は回収するが、未検証 locator は close しない。

`Close` は caller の有限 deadline と保護された cleanup timeout の短い方を使う。
受信中なら run context を取り消して Reader の取消と completion 待機へ進み、受信本体がまだ動作中なら回収未確認を返す。
未完了 I/O の buffer、OVERLAPPED、event、handle は `bootstrapio.Reader` が保持する。
取消要求や handle close を completion と見なさず、参照を保持した同じ receiver に明示した有限 context で `Close` を再度呼ぶ。
回収完了後の通常の `Close` は繰り返し nil を返す。

`bootstrapauth.ErrFatal` と `protectedkey.ErrFatal` は `errors.Is` で伝わり、その receiver では消えない。
caller は `UnlockOSThread` せずにその goroutine を終了し、別 worker で認証や鍵取得をやり直さない。
鍵 scope の fatal 以後も、Peer と Reader の既所有参照だけを回収する。
OS API 自体が戻らない故障に対する強制終了は、この source 検証の保証に含まれない。

## 検証した境界

全 OS 共通の試験は private callback に fake を入れ、公開 `ReadApproved` を観察する。
成功試験も実 factory の `Authenticate`、実 Reader、実 `BeginNativeScope`、実鍵の `Load` を呼ばない。
FakeSigner は公開 RFC8032 の公開鍵だけを持ち、秘密鍵や署名を生成しない。

同一入力の red/green は、認証から全回収までの成功、鍵取得失敗後の peer 再検査、鍵取得前の取消拒否、2000 年の期限切れ grant による鍵取得前の拒否の 4 組である。
Native の成功 fixture は非運用の 2100 年の固定期限を使い、純粋 codec の既存 fixture は変更しない。
元の絶対期限が peer の前後検査、鍵 scope、Load に共通して適用されることも回帰試験で確認する。
最初の red はコンパイル成功後、意味検証の仮拒否で失敗した。
追加の回帰試験は全 argv 相関値、構造異常、mode/digest、全保護上限、catalog、認証拒否、adopt 失敗時の所有保持、scope/peer/Reader 回収失敗、fatal の保持、並行 Close を確認する。
通常試験、race、vet、Windows amd64/arm64 の test binary crosscompile は個別の証跡とする。

実 Windows DLL、SCM、pipe、service account、ACL、鍵配置、SSH、製品起動を実行していない。
特に EOF 後の peer 再検査でも pipe PID の再照会を必須とし、問い合わせ失敗は拒否する。
writer の全 close 後にも実 Windows がこの問い合わせを満たすかは未実証であり、可用性のために検査を省略しない。
fake 試験と crosscompile の成功を実環境の認証成立や製品有効化へ広げない。
