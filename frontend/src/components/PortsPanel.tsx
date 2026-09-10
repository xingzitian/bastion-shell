import { useEffect, useState } from 'react'
import type { PortForwardRule } from '../shared/types'
import { useForwardStore } from '../store/forwards'

interface Props {
  profileId: string
  connectionId: string | null
  onClose: () => void
}

const STATUS_TEXT: Record<string, string> = {
  idle: '未启动',
  listening: '转发中',
  error: '失败'
}

const EMPTY_FORM = {
  label: '',
  localHost: '127.0.0.1',
  localPort: '',
  remoteHost: '127.0.0.1',
  remotePort: '',
  enabled: true
}

/** 端口转发面板（解耦：独立组件 + 独立 store，只依赖 profileId/connectionId） */
export function PortsPanel({ profileId, connectionId, onClose }: Props) {
  const rules = useForwardStore((s) => s.rules)
  const statuses = useForwardStore((s) => s.statuses)
  const messages = useForwardStore((s) => s.messages)

  const [editingId, setEditingId] = useState<string | null>(null)
  const [label, setLabel] = useState(EMPTY_FORM.label)
  const [localHost, setLocalHost] = useState(EMPTY_FORM.localHost)
  const [localPort, setLocalPort] = useState(EMPTY_FORM.localPort)
  const [remoteHost, setRemoteHost] = useState(EMPTY_FORM.remoteHost)
  const [remotePort, setRemotePort] = useState(EMPTY_FORM.remotePort)
  const [enabled, setEnabled] = useState(EMPTY_FORM.enabled)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    void window.bastion.listForwardRules(profileId).then((rs) => useForwardStore.getState().setRules(rs))
  }, [profileId])

  const resetForm = (): void => {
    setEditingId(null)
    setLabel(EMPTY_FORM.label)
    setLocalHost(EMPTY_FORM.localHost)
    setLocalPort(EMPTY_FORM.localPort)
    setRemoteHost(EMPTY_FORM.remoteHost)
    setRemotePort(EMPTY_FORM.remotePort)
    setEnabled(EMPTY_FORM.enabled)
    setErr(null)
  }

  const startEdit = (rule: PortForwardRule): void => {
    // 编辑前先停掉正在运行的转发，避免旧配置继续跑
    if ((statuses[rule.id] ?? 'idle') === 'listening') {
      void window.bastion.stopForward(rule.id)
    }
    setEditingId(rule.id)
    setLabel(rule.label)
    setLocalHost(rule.localHost)
    setLocalPort(String(rule.localPort))
    setRemoteHost(rule.remoteHost)
    setRemotePort(String(rule.remotePort))
    setEnabled(rule.enabled)
    setErr(null)
  }

  const saveRule = async (): Promise<void> => {
    const lp = Number(localPort)
    const rp = Number(remotePort)
    if (!label.trim()) return setErr('请填标签')
    if (!Number.isInteger(lp) || lp < 1 || lp > 65535) return setErr('本地端口无效（1-65535）')
    if (!remoteHost.trim()) return setErr('请填远端主机')
    if (!Number.isInteger(rp) || rp < 1 || rp > 65535) return setErr('远端端口无效（1-65535）')
    const editing = editingId ? rules.find((r) => r.id === editingId) : null
    const rule: PortForwardRule = {
      id: editing?.id ?? crypto.randomUUID(),
      profileId,
      label: label.trim(),
      localHost,
      localPort: lp,
      remoteHost: remoteHost.trim(),
      remotePort: rp,
      enabled
    }
    const saved = await window.bastion.saveForwardRule(profileId, rule)
    useForwardStore.getState().upsertRule(saved)
    resetForm()
  }

  const toggle = async (rule: PortForwardRule): Promise<void> => {
    const st = statuses[rule.id] ?? 'idle'
    if (st === 'listening') {
      await window.bastion.stopForward(rule.id)
    } else {
      if (!connectionId) return setErr('无已连接会话，无法启动转发')
      const r = await window.bastion.startForward(connectionId, rule)
      if (!r.ok) setErr(r.message)
    }
  }

  const remove = async (rule: PortForwardRule): Promise<void> => {
    await window.bastion.deleteForwardRule(rule.id)
    useForwardStore.getState().removeRule(rule.id)
    if (editingId === rule.id) resetForm()
  }

  const open = (rule: PortForwardRule): void => {
    const host = rule.localHost === '0.0.0.0' ? '127.0.0.1' : rule.localHost
    void window.bastion.openExternal(`http://${host}:${rule.localPort}`)
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal fw-modal" onClick={(e) => e.stopPropagation()}>
        <h2>端口转发</h2>
        <div className="muted" style={{ fontSize: 12 }}>
          本地端口 → 服务器可达的远端 host:port（复用当前 SSH 连接隧道）
        </div>

        <div className="fw-form">
          <label className="field">
            标签
            <input type="text" value={label} placeholder="例如 redis / web 8080" onChange={(e) => setLabel(e.target.value)} />
          </label>
          <div className="row">
            <label className="field">
              本地绑定
              <select value={localHost} onChange={(e) => setLocalHost(e.target.value)}>
                <option value="127.0.0.1">127.0.0.1（仅本机）</option>
                <option value="0.0.0.0">0.0.0.0（局域网）</option>
              </select>
            </label>
            <label className="field">
              本地端口
              <input type="number" value={localPort} min={1} max={65535} placeholder="8080" onChange={(e) => setLocalPort(e.target.value)} />
            </label>
          </div>
          <div className="row">
            <label className="field">
              远端主机
              <input type="text" value={remoteHost} placeholder="127.0.0.1 / 内网 IP" onChange={(e) => setRemoteHost(e.target.value)} />
            </label>
            <label className="field">
              远端端口
              <input type="number" value={remotePort} min={1} max={65535} placeholder="3306" onChange={(e) => setRemotePort(e.target.value)} />
            </label>
          </div>
          <label className="checkbox-field">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            连接建立后自动启动
          </label>
          <div className="modal-actions">
            {editingId && (
              <button className="btn" onClick={resetForm}>
                取消编辑
              </button>
            )}
            <button className="btn primary" onClick={() => void saveRule()}>
              {editingId ? '保存修改' : '＋ 添加转发'}
            </button>
          </div>
          {err && <div className="error">{err}</div>}
        </div>

        <div className="fw-list">
          {rules.length === 0 && <div className="muted">暂无转发规则</div>}
          {rules.map((r) => {
            const st = statuses[r.id] ?? 'idle'
            return (
              <div key={r.id} className="fw-row">
                <div className="fw-main">
                  <div className="fw-title">{r.label || `${r.localPort}→${r.remotePort}`}</div>
                  <div className="fw-addr">
                    {r.localHost}:{r.localPort} → {r.remoteHost}:{r.remotePort}
                    {st === 'error' && messages[r.id] ? <span className="error"> · {messages[r.id]}</span> : null}
                  </div>
                </div>
                <span className={`fw-status st-${st}`}>{STATUS_TEXT[st] ?? st}</span>
                <button className="mini-btn" onClick={() => void toggle(r)}>
                  {st === 'listening' ? '停止' : '启动'}
                </button>
                {st === 'listening' && (
                  <button className="mini-btn" title="在浏览器打开" onClick={() => open(r)}>
                    ↗
                  </button>
                )}
                <button className="mini-btn" onClick={() => startEdit(r)}>
                  编辑
                </button>
                <button className="mini-btn danger" onClick={() => void remove(r)}>
                  删除
                </button>
              </div>
            )
          })}
        </div>

        <div className="modal-actions">
          <button className="btn" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
