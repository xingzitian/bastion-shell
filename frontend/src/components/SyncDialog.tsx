import { useState } from 'react'

interface Props {
  sessionId: string
  onClose: () => void
  onDone: (ok: boolean, message: string) => void
}

/** 目录同步（rsync 传输桥）：本地 ↔ 目标机，块级增量 */
export function SyncDialog({ sessionId, onClose, onDone }: Props) {
  const [localPath, setLocalPath] = useState('')
  const [remotePath, setRemotePath] = useState('.')
  const [direction, setDirection] = useState<'upload' | 'download'>('upload')
  const [busy, setBusy] = useState(false)

  const start = async (): Promise<void> => {
    if (!localPath.trim()) {
      onDone(false, '请填写本地路径（Windows 路径，如 C:\\dev\\project）')
      return
    }
    if (!remotePath.trim()) {
      onDone(false, '请填写远端路径')
      return
    }
    setBusy(true)
    try {
      const r = await window.bastion.syncDirectory({
        sessionId,
        localPath: localPath.trim(),
        remotePath: remotePath.trim(),
        direction
      })
      onDone(r.ok, r.message)
    } catch (err) {
      onDone(false, err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
      onClose()
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>目录同步（rsync 增量）</h2>
        <p className="muted" style={{ fontSize: 12 }}>
          经当前堡垒机会话做块级增量同步。前提：目标机已装 rsync、本机 WSL 已装 rsync；同步时当前终端需停在目标机 shell 提示符。
        </p>

        <label className="field-label">方向</label>
        <select value={direction} onChange={(e) => setDirection(e.target.value as 'upload' | 'download')}>
          <option value="upload">上传（本地 → 远端）</option>
          <option value="download">下载（远端 → 本地）</option>
        </select>

        <label className="field-label">本地路径（Windows）</label>
        <input
          type="text"
          value={localPath}
          onChange={(e) => setLocalPath(e.target.value)}
          placeholder="C:\dev\project"
          spellCheck={false}
        />

        <label className="field-label">远端路径</label>
        <input
          type="text"
          value={remotePath}
          onChange={(e) => setRemotePath(e.target.value)}
          placeholder=". 或 /home/wsl/app"
          spellCheck={false}
        />

        <div className="modal-actions">
          <button className="btn" onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={busy} onClick={() => void start()}>
            {busy ? '同步中…' : '开始同步'}
          </button>
        </div>
      </div>
    </div>
  )
}
