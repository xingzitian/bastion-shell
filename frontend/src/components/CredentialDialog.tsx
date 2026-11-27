import { useCallback, useEffect, useState } from 'react'

/**
 * 桌面版「凭据」弹窗（与扩展的凭据面板同一份数据：`/api/credentials`）。
 *
 * 回答三件事：
 *   1. 每个档案到底存了没有（密码 / 私钥口令），存在哪一边（桌面 DPAPI / 扩展钥匙串）；
 *   2. 能在这里设置、替换、查看、删除**桌面自己**那份；
 *   3. 改名/删除档案后留下的**孤儿**能看见并清掉（扩展那份只能提示去扩展里删）。
 *
 * ⚠️ 交互都不依赖 window.prompt / confirm：WebView2 里宿主没转发脚本对话框时
 * 它们会**静默返回 null**，功能看起来"点了没反应"。所以用弹窗内的行内输入 + 两步确认。
 */

interface Slot {
  has: boolean
  owners: string[]
  updatedAt: string
}

interface Row {
  name: string
  target: string
  authMethod: string
  password: Slot
  passphrase: Slot
}

interface Orphan {
  key: string
  owners: string[]
  kinds: string[]
  updatedAt: string
}

interface View {
  rows: Row[]
  orphans: Orphan[]
  indexFile: string
  note: string
}

const ownerText = (owners: string[]): string =>
  owners.length === 0 ? '' : owners.map((o) => (o === 'desktop' ? '桌面' : '扩展')).join(' + ')

function slotText(slot: Slot): string {
  if (!slot.has) return '未存'
  const who = ownerText(slot.owners)
  const when = slot.updatedAt ? ' · ' + slot.updatedAt.slice(0, 16).replace('T', ' ') : ''
  return '已存（' + who + when + '）'
}

export function CredentialDialog({ onClose }: { onClose: () => void }) {
  const [view, setView] = useState<View | null>(null)
  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')
  /** 正在编辑哪个档案的密码（行内输入框） */
  const [editing, setEditing] = useState<{ name: string; value: string } | null>(null)
  /** 正在查看哪个档案的密码（行内只读框，关掉即消失） */
  const [revealing, setRevealing] = useState<{ name: string; value: string } | null>(null)
  /** 二次确认：要删的对象（档案名或孤儿键） */
  const [confirmDel, setConfirmDel] = useState('')

  const load = useCallback(async () => {
    try {
      const v = await window.bastion.getCredentials()
      setView({
        rows: v?.rows ?? [],
        orphans: v?.orphans ?? [],
        indexFile: v?.indexFile ?? '',
        note: v?.note ?? ''
      })
    } catch (e) {
      setErr('读取凭据失败：' + (e instanceof Error ? e.message : String(e)))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const savePassword = async (name: string): Promise<void> => {
    const value = editing && editing.name === name ? editing.value : ''
    if (!value) {
      setErr('密码不能为空')
      return
    }
    setErr('')
    try {
      await window.bastion.setPassword(name, value)
      setEditing(null)
      setMsg(`已保存「${name}」的密码（桌面 DPAPI）`)
      await load()
    } catch (e) {
      setErr('保存失败：' + (e instanceof Error ? e.message : String(e)))
    }
  }

  const reveal = async (name: string): Promise<void> => {
    setErr('')
    try {
      const pw = await window.bastion.getPassword(name)
      if (!pw) {
        setMsg(`「${name}」桌面这边没有存密码（可能是扩展存的）`)
        return
      }
      setRevealing({ name, value: pw })
    } catch (e) {
      setErr('读取失败：' + (e instanceof Error ? e.message : String(e)))
    }
  }

  const doDelete = async (key: string): Promise<void> => {
    setErr('')
    try {
      await window.bastion.deletePassword(key)
      setConfirmDel('')
      setMsg(`已删除「${key}」的桌面密码 —— 下次连接会重新问`)
      await load()
    } catch (e) {
      setErr('删除失败：' + (e instanceof Error ? e.message : String(e)))
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal cred-modal" onClick={(e) => e.stopPropagation()}>
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 style={{ margin: 0 }}>凭据</h2>
          <button className="btn" onClick={onClose}>
            关闭
          </button>
        </div>
        {view ? <div className="ai-note">{view.note}</div> : null}
        {msg ? <div className="muted">{msg}</div> : null}
        {err ? <div className="deploy-error">{err}</div> : null}

        {!view ? (
          <div className="muted">读取中…</div>
        ) : (
          <>
            <table className="cred-table">
              <thead>
                <tr>
                  <th>档案</th>
                  <th>认证</th>
                  <th>密码</th>
                  <th>私钥口令</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {view.rows.map((r) => (
                  <tr key={r.name}>
                    <td>
                      <b>{r.name}</b>
                      <div className="muted">{r.target}</div>
                      {editing && editing.name === r.name ? (
                        <div className="cred-edit">
                          <input
                            type="password"
                            autoFocus
                            placeholder={`「${r.name}」的新密码`}
                            value={editing.value}
                            onChange={(e) => setEditing({ name: r.name, value: e.target.value })}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter') void savePassword(r.name)
                              if (e.key === 'Escape') setEditing(null)
                            }}
                          />
                          <button className="btn" onClick={() => void savePassword(r.name)}>
                            保存
                          </button>
                          <button className="btn" onClick={() => setEditing(null)}>
                            取消
                          </button>
                        </div>
                      ) : null}
                      {revealing && revealing.name === r.name ? (
                        <div className="cred-edit">
                          <input type="text" readOnly value={revealing.value} onFocus={(e) => e.target.select()} />
                          <button className="btn" onClick={() => setRevealing(null)}>
                            收起
                          </button>
                        </div>
                      ) : null}
                    </td>
                    <td>{r.authMethod === 'key' ? '密钥' : '密码'}</td>
                    <td>{slotText(r.password)}</td>
                    <td>{slotText(r.passphrase)}</td>
                    <td className="cred-ops">
                      <button className="btn" onClick={() => setEditing({ name: r.name, value: '' })}>
                        {r.password.has ? '替换密码' : '设置密码'}
                      </button>
                      {r.password.has ? (
                        <button className="btn" onClick={() => void reveal(r.name)}>
                          查看
                        </button>
                      ) : null}
                      {r.password.has && r.password.owners.includes('desktop') ? (
                        confirmDel === r.name ? (
                          <>
                            <button className="btn danger" onClick={() => void doDelete(r.name)}>
                              确认删除
                            </button>
                            <button className="btn" onClick={() => setConfirmDel('')}>
                              取消
                            </button>
                          </>
                        ) : (
                          <button className="btn" onClick={() => setConfirmDel(r.name)}>
                            删除
                          </button>
                        )
                      ) : null}
                    </td>
                  </tr>
                ))}
                {view.rows.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="muted">
                      还没有档案。
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>

            <h3 style={{ marginTop: 16 }}>孤儿凭据（{view.orphans.length}）</h3>
            <div className="muted">
              这些键还存着密码，但档案表里已经没有这个名字了（改过名 / 删过档案）。改名的话，下次在对应产品里保存档案会自动搬；
              这里可以直接清掉。
            </div>
            <table className="cred-table">
              <thead>
                <tr>
                  <th>名字</th>
                  <th>存在哪</th>
                  <th>种类</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {view.orphans.map((o) => (
                  <tr key={o.key}>
                    <td>
                      <b>{o.key}</b>
                    </td>
                    <td>{ownerText(o.owners)}</td>
                    <td>{o.kinds.join(' / ')}</td>
                    <td className="cred-ops">
                      {o.owners.includes('desktop') ? (
                        confirmDel === o.key ? (
                          <>
                            <button className="btn danger" onClick={() => void doDelete(o.key)}>
                              确认删除
                            </button>
                            <button className="btn" onClick={() => setConfirmDel('')}>
                              取消
                            </button>
                          </>
                        ) : (
                          <button className="btn danger" onClick={() => setConfirmDel(o.key)}>
                            删除（桌面）
                          </button>
                        )
                      ) : (
                        <span className="muted">到 VS Code 扩展里删</span>
                      )}
                    </td>
                  </tr>
                ))}
                {view.orphans.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="muted">
                      没有孤儿凭据。
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
            <div className="muted" style={{ marginTop: 10 }}>
              清单文件：{view.indexFile}（只有元数据，不含密码）
            </div>
          </>
        )}
      </div>
    </div>
  )
}
