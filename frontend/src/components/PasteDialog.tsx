import { useState } from 'react'
import type { PasteChoice } from '../lib/smartPaste'

interface Props {
  text: string
  lines: number
  onChoose: (mode: PasteChoice, remember: boolean) => void
  onCancel: () => void
}

/** 多行粘贴三选一：原样 / 合并为一行 / 取消（可记住选择，Shift+粘贴可重新弹出） */
export function PasteDialog({ text, lines, onChoose, onCancel }: Props) {
  const [remember, setRemember] = useState(false)
  const preview = text.length > 240 ? `${text.slice(0, 240)}…` : text
  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>多行粘贴（{lines} 行）</h2>
        <pre className="paste-preview">{preview}</pre>
        <div className="muted" style={{ fontSize: 12 }}>
          原样粘贴 = 保留换行逐行发送（续行符/heredoc 场景）；合并为一行 = 换行替换为空格（适合 curl | bash 这类命令）
        </div>
        <label className="checkbox-field">
          <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
          记住我的选择（按住 Shift 粘贴可重新弹出此框）
        </label>
        <div className="modal-actions">
          <button className="btn" onClick={onCancel}>
            取消
          </button>
          <button className="btn" onClick={() => onChoose('raw', remember)}>
            原样粘贴
          </button>
          <button className="btn primary" onClick={() => onChoose('merged', remember)}>
            合并为一行
          </button>
        </div>
      </div>
    </div>
  )
}
