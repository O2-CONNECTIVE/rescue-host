# 固定provisioning sourceの準備と未設定の入口

初期評価の対象は、別の非管理者guestによる監督・停止処理への干渉である。
host operator/adminの既侵害とadmin guestは保証範囲に含めない。
製品gate、実Windowsでの保護成立、既存44条件の判定は変更しない。

## 入力と信頼根

`Anchor`は保護された配布・配置側が固定するservice名、recordと独立digestの絶対path、期待volume、有限の読取り・回収予算である。
`fixedAnchor`は全値未設定であり、環境変数、argv、UI、current directory、実行fileの所在から補完しない。
実配置先、実SID、実volume serialをこのsourceで選定してはいない。

この候補の信頼根はOSが保護する固定配置であり、runtimeでの署名検証ではない。
recordとそのSHA-256を同じ未検証fileから取り出して信頼する方式ではなく、別の固定digest fileも同じ保護条件で検査する。
両fileを同時に更新するtransactionは提供しないため、更新途中の不一致は拒否する。
host operator/adminによる両fileの巻戻しを防ぐjournalでもない。

正規の配布・配置者は、実行file・record・digestの出所とintegrityを独立に確認し、guestに変更される前から固定配置を保護する必要がある。
配布署名の検証、配布物と期待hashの照合方法、実行主体・service object/process/token objectの保護を実機設定とともに確定する必要がある。
現在のACL照会だけで、配置前の改変、既存の書込みhandle、OS管理者の介入を排除したとはいえない。
service自身のimage hashは独立recordに置き、実行fileへそのfile自身のhashを埋め込む循環を作らない。

## 固定record候補

`rescue-service-provisioning/1`は固定fileの非秘密の形式であり、operator承認wire、既存control/1、bootstrap/1を変更しない。
UTF-8の単一object、最大256KiB、深さ16、先頭`{`と末尾`}`に任意の単一LFだけを認める。
未知・重複・欠落・大小文字違いのfield、不正UTF-8、null、余剰record、BOM、範囲外の値は拒否する。
整数とdurationのnanosecond値は正規10進文字列、時刻は正規UTC、hashは小文字hexとして扱う。
正確なfield集合は`record_decode.go`の型が所有する候補であり、共有schemaへ独自の承認commandを追加しない。

recordには固定profile、service/process保護方針、固定artifactのpath/hashと祖先、既存host鍵参照と公開fingerprint、許可raw Ed25519公開鍵、backend上限と各予算だけを含める。
秘密鍵、token/handle、grant、Identity、boot epoch、保存済み承認、絶対hard deadlineは含めない。
`DecodeRecord`と`ValidateRecord`の成功は構造検証だけで、配置元の認証や実行許可ではない。

## 接続するsourceと残る実行条件

`PrepareFixed`はcallerが固定する元OS threadで固定rootを開き、digest照合後のrecordを復号し、観測したservice/user SIDとrecordの一致を要求する。
そのquery-only primary tokenを既存`bootstrapauth.PrepareService`の照合に借用し、現在のservice imageと配置証拠を検査する。
取得途中の失敗でも返した`Prepared`が同じ所有参照を保持し、同じthreadの`Close`で回収する。
fatalならthreadを再利用せず、未確認の回収を成功へ変換しない。
保持中の`Prepared.ComposeService`は同じrecordを再照合後に既存Serviceの構成へ渡し、部分準備・Close後・fatalの状態から構成を作らない。
これは起動用tokenを発行する機能ではなく、その別の保護供給元と単一SCM ownerが必要である。

`ComposeService`は、保護供給元が別途所有する既存primary handleと認証済み承認sourceを受け取る純粋constructorである。
同じ固定recordからnative stdio、native bootstrap、JobRuntime、Serviceを組み合わせる。
構成では承認sourceを読まず、issuer・grant・listener・processを生成しない。
入力が正規の供給元から来たことやtokenに必要な権限があることをconstructorの成功で証明しない。

`rescue-service-host`の既定入口は、固定Anchorが未設定ならnative APIより前に拒否する。
固定値を後から配置しただけでも、起動token・承認入口・SCM全体の所有回収が未設定の間はnative rootの読取りにもSCMにも進まない。
query-only handleを起動用handleへ転用するfallbackはない。
新しいserviceの起動は許可0であり、保存recordから旧grantやdeadlineを復元しない。
UI pollingを承認・renewへ変換しない。

次の配線に必要なのは以下である。

- process/token objectへのguestの危険な操作を拒否する実検査と、必要権限を持つ既存primary handleの取得・保持・回収境界。設定読取用`TOKEN_QUERY`とは分ける。
- service側とoperator側の双方の認証、有限codec、新しい人の承認を発行する入口。既存localcontrolのhello/snapshot/stopをStartやrenewへ拡張しない。
- SCMの全回収確認まで上記所有物を保持し、未確認なら参照を捨ててexitしない実行owner。
- 固定profileの実値と、child側の保護profile供給・承認済みtransportの実供給。仮の成功callbackや秘密値のserializationで補完しない。

## 検証の境界

構造検証とnative API seamの通常unit/race試験、およびWindows向けcompileを扱う。
実service登録・起動、token/ACL/account/FW変更、鍵作成・実鍵読取り、SSH接続は行わない。
実WindowsのACL、reparse拒否、元thread、取消completion、独立guest保護の成立は未実証である。
通常CIをP2専用試験、Job最後handle、物理停止、監督hangやOS sleepの証拠へ読み替えない。
