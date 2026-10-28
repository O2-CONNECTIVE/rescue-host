import { ArrowRightLeft, CircleAlert, CircleCheck, CircleDashed, Clock3, LoaderCircle, Square } from 'lucide-react';
import type { Phase } from './model.ts';

export const status = {
  entry: { title: 'サンプルで接続の流れを試す', text: '実際の接続は行いません。受取りから終了確認まで、表示と操作を体験できます。', role: '', icon: CircleDashed, step: 0 },
  review: { title: '接続前の確認', text: '接続先・許可範囲・期限を確認してから、接続先の操作ユーザーへ承認を依頼します。ここではすべて架空のデータです。', role: '', icon: CircleDashed, step: 0 },
  waiting: { title: '承認を待っています（モック）', text: '依頼の受付は、承認や接続の完了ではありません。モックの承認応答を待っています。', role: 'connected', icon: LoaderCircle, step: 1 },
  connecting: { title: '承認済み・接続準備中（モック）', text: '承認応答を受け取りました。接続完了はまだ確認していません。', role: 'connected', icon: LoaderCircle, step: 1 },
  ready: { title: '接続情報の準備完了（モック）', text: '接続情報をコピーして、外部エージェントへ渡す流れを試せます。このGUIはSSHを起動しません。', role: 'connected', icon: ArrowRightLeft, step: 2 },
  connected: { title: '接続中（モック）', text: 'モックの接続完了応答を表示しています。「接続中」は安全保証や認可の証明ではありません。', role: 'connected', icon: ArrowRightLeft, step: 2 },
  stopping: { title: '停止を要求しました（モック）', text: '停止要求を受け付けました。終了確認の応答はまだありません。', role: 'stopping', icon: Square, step: 3 },
  stopped: { title: '終了確認済み（モック）', text: 'モックの終了確認応答を受け取りました。実通信の停止や、作業内容の取消を証明するものではありません。', role: 'terminated', icon: CircleCheck, step: 3 },
  expired: { title: '期限切れ・終了未確認（モック）', text: '期限切れの応答を受け取りました。期限の表示だけでは、通信が終了したとは確認できません。', role: 'unconfirmed', icon: Clock3, step: 3 },
  failed: { title: '接続に失敗しました（モック）', text: '承認拒否または接続失敗の応答を受け取りました。通信終了の確認はありません。', role: 'unconfirmed', icon: CircleAlert, step: 1 },
  unconfirmed: { title: '終了を確認できません（モック）', text: '応答を確認できないため、終了確認済みとは表示しません。この体験の未確認状態を保持します。', role: 'unconfirmed', icon: CircleAlert, step: 3 }
} satisfies Record<Phase, { title: string; text: string; role: string; icon: typeof CircleDashed; step: number }>;
