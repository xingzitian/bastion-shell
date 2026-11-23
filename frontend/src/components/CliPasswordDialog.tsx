import { useState } from 'react'

/**
 * 单条会话的密码框（点 ⚡ 时，如果档案是密码认证/已存密码，就先问一次）。
 *
 * 为什么需要：桌面 spawn 出来的 node **没有控制台**，CLI 没法在终端里问密码；
 * 而「🗂 组」里已经有密码框，⚡ 单条却会直接失败 —— 这里把两边对齐。
 *
 * 密码只经 `POST /api/shell/cli/start` 交给 Go，再由 Go 走子进程 **stdin**（不进 argv）。
 */

export function CliPasswordDialog({
  profile,
  onStart,
  onClose
}: {
  profile: string
  /** 参数是密码（空串 = 不带，交给 key 认证） */
  onStart: (password: string) => void
  onClose: () => void
}) {
  const [pw, setPw] = useState('')
  return (
    <div className="modal-mask" onClick={onClose}>
      <div className="modal cli-pw-dialog" onClick={(e) => e.stopPropagation()}>
        <h3>档案「{profile}」需要密码</h3>
        <p className="muted">
          这个档案是密码认证（或存过密码）。桌面启动的 CLI 没有控制台，问不了密码，所以在这里填一次。
          密码只在内存里，经 stdin 传给子进程，不落盘。
        </p>
        <input
          className="cli-pw-input"
          type="password"
          autoFocus
          autoComplete="off"
          placeholder="密码（含 MFA 的机器：先填密码，动态码等下在终端里输）"
          value={pw}
          onChange={(e) => setPw(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onStart(pw)
          }}
        />
        <div className="modal-actions">
          <button className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" onClick={() => onStart(pw)}>
            启动
          </button>
        </div>
      </div>
    </div>
  )
}
