import { useMemo, useState } from 'react'
import { parseIpList } from '../shared/ipList'

interface Props {
  hostLabel: string
  onCancel: () => void
  onStart: (opts: { hosts: string[]; autoSelect: boolean; autoBroadcast: boolean }) => void
}

const MAX_BATCH = 30

/**
 * 批量连接弹窗：粘贴 IP 列表（逗号/换行分隔），一次性在已认证连接上
 * 开多个会话；可选自动在堡垒机菜单输入 IP 回车（搜索式菜单），
 * 并默认全部加入广播输入（批量部署时敲一次命令群发）。
 */
export function BatchConnectDialog({ hostLabel, onCancel, onStart }: Props) {
  const [text, setText] = useState('')
  const [autoSelect, setAutoSelect] = useState(true)
  const [autoBroadcast, setAutoBroadcast] = useState(true)

  const parsed = useMemo(() => parseIpList(text, MAX_BATCH), [text])

  const submit = () => {
    if (parsed.hosts.length === 0) return
    onStart({ hosts: parsed.hosts, autoSelect, autoBroadcast })
  }

  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>批量连接</h2>
        <div className="muted" style={{ fontSize: 12 }}>
          通过 <b>{hostLabel}</b>（已认证，免 MFA）一次开多个会话
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <div className="modal-body">
            <textarea
              className="batch-input"
              value={text}
              placeholder={'粘贴 IP / 主机名，逗号、分号、空格或换行分隔：\n\n10.0.0.1, 10.0.0.2, 10.0.0.3\n10.0.0.4'}
              onChange={(e) => setText(e.target.value)}
              spellCheck={false}
            />
            <div className="muted" style={{ fontSize: 12 }}>
              已识别 {parsed.hosts.length} 台{parsed.skipped > 0 ? `（跳过重复/超限 ${parsed.skipped} 项）` : ''}
              {parsed.hosts.length >= MAX_BATCH ? `，单批上限 ${MAX_BATCH} 台` : ''}
            </div>
            <label className="checkbox-field">
              <input type="checkbox" checked={autoSelect} onChange={(e) => setAutoSelect(e.target.checked)} />
              自动在堡垒机菜单输入 IP 并回车（适用于搜索式菜单如 koko 的 [Host]&gt;；数字列表菜单需手动选）
            </label>
            <label className="checkbox-field">
              <input type="checkbox" checked={autoBroadcast} onChange={(e) => setAutoBroadcast(e.target.checked)} />
              创建后全部加入广播输入（📡）——批量部署时敲一次命令群发所有机器
            </label>
          </div>
          <div className="modal-actions">
            <button type="button" className="btn" onClick={onCancel}>
              取消
            </button>
            <button type="submit" className="btn primary" disabled={parsed.hosts.length === 0}>
              连接 {parsed.hosts.length > 0 ? `${parsed.hosts.length} 台` : ''}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
