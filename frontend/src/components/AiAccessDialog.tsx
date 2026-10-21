import { useState } from 'react'
import type { AiAccess } from '../shared/types'

interface Props {
  data: AiAccess
  onRefresh: () => Promise<void>
  onClose: () => void
}

/**
 * 「AI 访问」页（桌面版）。
 *
 * 与 VS Code 扩展的「AI 访问」树**同一份语义**，但桌面版只有**一个令牌**（全权）：
 * 所以这一页重点回答两件事 ——
 *   1. 现在哪个 AI 绑在哪条会话上（以及一键解绑）；
 *   2. 端点/token 是什么、怎么复制给 AI 客户端。
 *
 * 权限档的人话文案与扩展侧一致；桌面版没有分档时明确写出来，避免让人以为两边一样。
 */
export function AiAccessDialog({ data, onRefresh, onClose }: Props) {
  const [busy, setBusy] = useState('')
  const [msg, setMsg] = useState('')

  const unbind = async (clientId: string, alias?: string, sessionId?: string) => {
    setBusy(clientId + (alias ?? sessionId ?? ''))
    setMsg('')
    try {
      const r = await window.bastion.unbindAiBinding({ clientId, alias, sessionId })
      setMsg(r.ok ? '已解绑' : '没找到这条绑定（可能已经被清掉了）')
      await onRefresh()
    } catch (e) {
      setMsg('解绑失败：' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy('')
    }
  }

  const copyConfig = async () => {
    const cfg = {
      mcpServers: {
        bastionshell: {
          type: 'http',
          url: data.endpoint,
          headers: { Authorization: 'Bearer ' + data.token }
        }
      }
    }
    try {
      await navigator.clipboard.writeText(JSON.stringify(cfg, null, 2))
      setMsg('已复制客户端配置（含 token，别贴给别人）')
    } catch {
      setMsg('复制失败：请手动复制')
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal ai-modal" onClick={(e) => e.stopPropagation()}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 style={{ margin: 0 }}>AI 访问</h2>
          <button className="btn" onClick={onClose}>
            关闭
          </button>
        </div>

        <div className="ai-note">{data.note}</div>

        <div className="ai-endpoint">
          <div>
            <span className="muted">端点：</span>
            <code>{data.endpoint}</code>
          </div>
          <div>
            <span className="muted">令牌：</span>
            <code>{data.tokenMasked}</code>
          </div>
          <div className="row" style={{ gap: 8, marginTop: 6 }}>
            <button className="btn primary" onClick={copyConfig}>
              复制客户端配置
            </button>
            <button className="btn" onClick={() => onRefresh()}>
              刷新
            </button>
          </div>
        </div>

        {msg && <div className="ai-msg">{msg}</div>}

        <h3>客户端与绑定</h3>
        {data.clients.length === 0 && <div className="muted">还没有任何 AI 绑定会话。</div>}
        {data.clients.map((c) => (
          <div className="card ai-client" key={c.clientId}>
            <div className="row" style={{ justifyContent: 'space-between' }}>
              <span className="ai-client-name">
                {c.label}
                {c.external && <span className="badge">扩展侧</span>}
              </span>
              <span className={'badge ' + (c.isMain ? 'ok' : '')}>{c.scopeLabel}</span>
            </div>
            <div className="muted">{c.plain}</div>
            {c.can && c.can.length > 0 && (
              <div className="ai-chips">
                <span className="muted">能：</span>
                {c.can.map((x) => (
                  <span className="chip ok" key={x}>
                    {x}
                  </span>
                ))}
              </div>
            )}
            {c.cannot && c.cannot.length > 0 && (
              <div className="ai-chips">
                <span className="muted">不能：</span>
                {c.cannot.map((x) => (
                  <span className="chip err" key={x}>
                    {x}
                  </span>
                ))}
              </div>
            )}
            {c.bindings.length === 0 ? (
              <div className="muted" style={{ marginTop: 4 }}>
                还没绑定会话（AI 用 bastion_connect / bastion_use 之后这里会出现）
              </div>
            ) : (
              c.bindings.map((b) => (
                <div className="ai-binding" key={b.alias + b.sessionId}>
                  <span>
                    <b>{b.alias}</b> → {b.sessionId} <span className="muted">{b.name}</span>
                  </span>
                  <button
                    className="btn"
                    disabled={busy !== ''}
                    onClick={() => unbind(c.clientId, b.alias, b.sessionId)}
                  >
                    解绑
                  </button>
                </div>
              ))
            )}
          </div>
        ))}

        <h3>会话与归属</h3>
        {data.sessions.length === 0 && <div className="muted">还没有会话。</div>}
        {data.sessions.map((s) => (
          <div className="ai-binding" key={s.id}>
            <span>
              {s.name} <span className="muted">{s.id}</span>
            </span>
            <span>
              {s.owners.length === 0 ? (
                <span className="muted">没有 AI 绑着它</span>
              ) : (
                s.owners.map((o) => (
                  <span className="chip" key={o.clientId + o.alias}>
                    {o.alias}（{o.label}）
                  </span>
                ))
              )}
            </span>
          </div>
        ))}

        <div className="muted" style={{ marginTop: 10, fontSize: 12 }}>
          绑定文件：<code>{data.bindingsFile}</code>（与 VS Code 扩展共用同一份）
        </div>
      </div>
    </div>
  )
}
