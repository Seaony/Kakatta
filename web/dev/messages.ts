export const messages = {
  'zh-CN': {
    development: '开发模式', fake: '本机假数据', paused: '调度已暂停', running: '调度运行中',
    unknown: '调度状态未知', connecting: '正在连接开发管理器', connectionError: '无法读取开发状态，将自动重试。',
    console: '开发控制台', expand: '展开开发控制台', collapse: '收起开发控制台',
    scheduler: '任务调度', pause: '暂停调度', resume: '开启调度', pending: '正在提交…',
    generation: '后端版本', status: '后端状态', lastError: '编译或后端错误',
    controlError: '控制请求失败，请重试。', logs: '最近日志', noLogs: '暂无日志',
    logLimit: '显示最近 100 条日志', fixture: '假数据故障', nextRun: '下次运行生效，不会自动运行任务。',
    normal: '恢复正常', expired: '登录失效', risk: '触发验证', missing: '页面缺字段', api_error: '接口错误',
    selectedFault: '当前故障', starting: '正在启动', building: '正在编译', compiling: '正在编译',
    reloading: '新代码已编译，等当前运行结束后切换', waiting: '新代码已编译，等当前运行结束后切换',
    reload_failed: '新版本启动失败，请查看错误和日志',
    shutdown_blocked: '尚未退出，当前运行结束后再按 Ctrl+C 重试',
    stopping: '正在停止', backendRunning: '运行中', error: '后端出错', otherPhase: '后端状态：',
    unavailable: '未确认', requestStatus: '响应状态',
  },
  en: {
    development: 'Development mode', fake: 'Local fake data', paused: 'Scheduler paused', running: 'Scheduler running',
    unknown: 'Scheduler status unknown', connecting: 'Connecting to development manager',
    connectionError: 'Cannot read development status. Retrying automatically.',
    console: 'Development console', expand: 'Expand development console', collapse: 'Collapse development console',
    scheduler: 'Task scheduler', pause: 'Pause scheduler', resume: 'Start scheduler', pending: 'Submitting…',
    generation: 'Backend generation', status: 'Backend status', lastError: 'Build or backend error',
    controlError: 'Control request failed. Please retry.', logs: 'Recent logs', noLogs: 'No logs yet',
    logLimit: 'Showing the latest 100 log entries', fixture: 'Fake-data faults',
    nextRun: 'Applies to the next run. Does not run a task automatically.',
    normal: 'Restore normal', expired: 'Session expired', risk: 'Verification required', missing: 'Missing page fields', api_error: 'API error',
    selectedFault: 'Current fault', starting: 'Starting', building: 'Building', compiling: 'Building',
    reloading: 'New code is built. Waiting for the current run to finish before switching.',
    waiting: 'New code is built. Waiting for the current run to finish before switching.',
    reload_failed: 'New backend failed to start. Check the error and logs.',
    shutdown_blocked: 'Not stopped yet. Wait for the current run to finish, then press Ctrl+C again.',
    stopping: 'Stopping', backendRunning: 'Running', error: 'Backend error', otherPhase: 'Backend status: ',
    unavailable: 'Unconfirmed', requestStatus: 'Response status',
  },
  ja: {
    development: '開発モード', fake: 'この端末のテストデータ', paused: 'スケジューラーは一時停止中', running: 'スケジューラーは実行中',
    unknown: 'スケジューラーの状態は不明', connecting: '開発マネージャーに接続中', connectionError: '開発状態を取得できません。自動的に再試行します。',
    console: '開発コンソール', expand: '開発コンソールを開く', collapse: '開発コンソールを閉じる',
    scheduler: 'タスクのスケジュール', pause: 'スケジューラーを一時停止', resume: 'スケジューラーを再開', pending: '送信中…',
    generation: 'バックエンドの世代', status: 'バックエンドの状態', lastError: 'ビルドまたはバックエンドのエラー',
    controlError: '操作を送信できませんでした。もう一度お試しください。', logs: '最近のログ', noLogs: 'ログはまだありません',
    logLimit: '最新のログを100件表示', fixture: 'テストデータの障害', nextRun: '次回の実行から反映されます。タスクを自動実行することはありません。',
    normal: '正常に戻す', expired: 'ログイン期限切れ', risk: '認証が必要', missing: 'ページの項目が欠落', api_error: 'APIエラー',
    selectedFault: '現在の障害', starting: '起動中', building: 'ビルド中', compiling: 'ビルド中',
    reloading: '新しいコードのビルドが完了しました。現在の実行が終わってから切り替えます。',
    waiting: '新しいコードのビルドが完了しました。現在の実行が終わってから切り替えます。',
    reload_failed: '新しいバックエンドを起動できませんでした。エラーとログを確認してください。',
    shutdown_blocked: 'まだ終了していません。現在の実行が終わってから、もう一度 Ctrl+C を押してください。',
    stopping: '停止中', backendRunning: '実行中', error: 'バックエンドでエラーが発生', otherPhase: 'バックエンドの状態：',
    unavailable: '未確認', requestStatus: '応答ステータス',
  },
} as const;

export type DevelopmentMessages = typeof messages[keyof typeof messages];

export function phaseText(phase: string, text: DevelopmentMessages): string {
  if (phase === 'running') return text.backendRunning;
  switch (phase) {
    case 'starting': case 'building': case 'compiling': case 'reloading': case 'waiting':
    case 'reload_failed': case 'shutdown_blocked': case 'stopping': case 'error': return text[phase];
    default: return text.otherPhase + phase;
  }
}
