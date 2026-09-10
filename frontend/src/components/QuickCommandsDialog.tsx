import { useState } from 'react'
import type { QuickCommand } from '../shared/types'

interface Props {
  commands: QuickCommand[]
  onSave: (cmd: QuickCommand) => Promise<void>
  onDelete: (id: string) => Promise<void>
  onClose: () => void
}

/** 快捷命令管理弹窗：新增 / 编辑 / 删除 */
export function QuickCommandsDialog({ commands, onSave, onDelete, onClose }: Props) {
  const [rows, setRows] = useState<QuickCommand[]>(() =>
    commands.map((c) => ({ ...c }))
  )

  const update = (id: string, patch: Partial<QuickCommand>) => {
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }

  const add = () => {
    setRows((rs) => [...rs, { id: crypto.randomUUID(), label: '', command: '' }])
  }

  const save = (cmd: QuickCommand) => {
    if (!cmd.label.trim() || !cmd.command.trim()) return
    void onSave({ ...cmd })
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal qc-modal" onClick={(e) => e.stopPropagation()}>
        <h2>快捷命令管理</h2>
        <div className="qc-list">
          {rows.length === 0 && <div className="muted">还没有快捷命令，点「＋ 新增」添加</div>}
          {rows.map((r) => (
            <div key={r.id} className="qc-row">
              <input
                type="text"
                className="qc-label"
                placeholder="名称（如：进 /data）"
                value={r.label}
                onChange={(e) => update(r.id, { label: e.target.value })}
              />
              <input
                type="text"
                className="qc-command"
                placeholder='命令（如：cd "/data"）'
                value={r.command}
                onChange={(e) => update(r.id, { command: e.target.value })}
              />
              <button className="mini-btn" onClick={() => save(r)} title="保存">
                保存
              </button>
              <button className="icon-btn" title="删除" onClick={() => void onDelete(r.id).then(() => setRows((rs) => rs.filter((x) => x.id !== r.id)))}>
                ✕
              </button>
            </div>
          ))}
        </div>
        <div className="modal-actions">
          <button className="btn" onClick={add}>
            ＋ 新增
          </button>
          <span style={{ flex: 1 }} />
          <button className="btn" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
