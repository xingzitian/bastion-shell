import { useEffect, useState } from 'react'

/**
 * 「会话组」对话框（B4）：勾几个档案 → 起**一个** CLI 宿主进程（serve a b c）
 * → 一个窗口里用顶栏下拉切会话，AI 只配一次 MCP 端点。
 *
 * 为什么不让每个档案各起一个进程：那样 MCP 端点会有 N 个，客户端要配 N 次 ——
 * 这正是"有人要用 MCP"时最烦的地方。
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
  onStart: (names: string[]) => void
  onClose: () => void
}) {
  const [picked, setPicked] = useState<string[]>([])

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
          勾选要一起连的档案。它们会在**同一个进程**里连上，共用一个 MCP 端点、一个网页终端
          （窗口顶栏可切换会话）。密码认证的档案会逐个询问。
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
            </li>
          ))}
        </ul>
        <div className="modal-actions">
          <button className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={picked.length === 0} onClick={() => onStart(picked)}>
            启动会话组（{picked.length} 条）
          </button>
        </div>
      </div>
    </div>
  )
}
