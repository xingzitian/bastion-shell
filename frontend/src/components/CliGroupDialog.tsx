import { useEffect, useState } from 'react'

/**
 * 「会话组」对话框（B4）：勾几个档案 → 起**一个** CLI 宿主进程（serve a b c）
 * → 一个窗口里用顶栏下拉切会话，AI 只配一次 MCP 端点。
 *
 * 密码认证的档案可以在这里填密码：它会经 `POST /api/shell/cli/start` 传给 Go，
 * 再由 Go **通过 stdin** 交给 CLI 子进程（不进 argv —— 进程列表里能看到 argv）。
 * 留空的话 CLI 会试图在终端里问，而 GUI 启动的 node 没有控制台，会失败。
 */

export interface GroupProfile {
  id: string
  name: string
  username?: string
  host?: string
  port?: number
}

export function CliGroupDialog({
  profiles,
  onStart,
  onClose
}: {
  profiles: GroupProfile[]
  /** 参数是「档案名 → 密码」（没填的就是空串） */
  onStart: (picked: Array<{ name: string; password: string }>) => void
  onClose: () => void
}) {
  const [picked, setPicked] = useState<string[]>([])
  const [pw, setPw] = useState<Record<string, string>>({})

  useEffect(() => {
    // 默认选中前两条，省得每次都点
    setPicked(profiles.slice(0, 2).map((p) => p.name))
  }, [profiles])

  const toggle = (name: string): void => {
    setPicked((cur) => (cur.includes(name) ? cur.filter((x) => x !== name) : [...cur, name]))
  }

  return (
    <div className="modal-mask" onClick={onClose}>
      <div className="modal cli-group-dialog" onClick={(e) => e.stopPropagation()}>
        <h3>会话组（CLI 宿主）</h3>
        <p className="muted">
          勾选要一起连的档案：它们会在**同一个进程**里连上，共用一个 MCP 端点、一个网页终端
          （窗口顶栏可切换会话）。密码认证的档案在这里填一次即可（只在内存里传给子进程）。
        </p>
        <ul className="cli-group-list">
          {profiles.map((p) => (
            <li key={p.id}>
              <label>
                <input type="checkbox" checked={picked.includes(p.name)} onChange={() => toggle(p.name)} />
                <span>
                  {p.name}
                  <span className="muted">
                    {' '}
                    {p.username}@{p.host}:{p.port}
                  </span>
                </span>
              </label>
              {picked.includes(p.name) ? (
                <input
                  className="cli-group-pw"
                  type="password"
                  autoComplete="off"
                  placeholder="密码（留空会由终端询问；GUI 下没有控制台会失败）"
                  value={pw[p.name] ?? ''}
                  onChange={(e) => setPw((cur) => ({ ...cur, [p.name]: e.target.value }))}
                />
              ) : null}
            </li>
          ))}
        </ul>
        <div className="modal-actions">
          <button className="btn" onClick={onClose}>
            取消
          </button>
          <button
            className="btn primary"
            disabled={picked.length === 0}
            onClick={() => onStart(picked.map((name) => ({ name, password: pw[name] ?? '' })))}
          >
            启动会话组（{picked.length} 条）
          </button>
        </div>
      </div>
    </div>
  )
}
