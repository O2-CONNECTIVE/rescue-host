import { ArrowRightLeft, CircleAlert, CircleCheck, CircleDashed, Clock3, LoaderCircle, Square } from 'lucide-react';
import type { Phase } from './model.ts';

export const status = {
  entry: { title: 'サンプルで接続の流れを試す', text: 'サンプルを入力して、接続の準備を試せます。', role: '', icon: CircleDashed, step: 0 },
  review: { title: '接続前の確認', text: '接続先・許可範囲・期限を確認してください。', role: '', icon: CircleDashed, step: 0 },
  waiting: { title: '承認を待っています（モック）', text: '接続先の操作ユーザーの承認を待っています。', role: 'connected', icon: LoaderCircle, step: 1 },
  connecting: { title: '承認済み・接続準備中（モック）', text: '承認応答を受け取りました。接続情報を準備しています。', role: 'connected', icon: LoaderCircle, step: 1 },
  ready: { title: '接続情報の準備完了（モック）', text: '接続情報をコピーして、外部エージェントへ渡してください。', role: 'connected', icon: ArrowRightLeft, step: 2 },
  connected: { title: '接続中（モック）', text: '作業を終えるときは、接続を停止してください。', role: 'connected', icon: ArrowRightLeft, step: 2 },
  stopping: { title: '停止を要求しました（モック）', text: '終了確認の応答を待っています。', role: 'stopping', icon: Square, step: 3 },
  stopped: { title: '終了確認済み（モック）', text: '終了確認の応答を受け取りました。次の接続を準備できます。', role: 'terminated', icon: CircleCheck, step: 3 },
  expired: { title: '期限切れ・終了未確認（モック）', text: '新しい接続は始められません。接続先へ終了状況を確認してください。', role: 'unconfirmed', icon: Clock3, step: 3 },
  failed: { title: '接続に失敗しました（モック）', text: '終了未確認のため、新しい接続は始められません。接続先へ終了状況を確認してください。', role: 'unconfirmed', icon: CircleAlert, step: 1 },
  unconfirmed: { title: '終了を確認できません（モック）', text: '新しい接続は始められません。同じ接続の状態を再確認してください。', role: 'unconfirmed', icon: CircleAlert, step: 3 }
} satisfies Record<Phase, { title: string; text: string; role: string; icon: typeof CircleDashed; step: number }>;
