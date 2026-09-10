import { useState } from 'react'
import type { ConnectionProfile } from '../shared/types'

export interface ConnectDialogResult {
  password: string | undefined
  save: boolean
  autoReconnect: boolean
  /** 密码留空且勾选保存 → 使用本机已加密保存的密码 */
  useStoredPassword: boolean
  /** true=保存密码；false=清除已存密码；undefined=不动 */
  savePassword: boolean | undefined
  authMethod: 'password' | 'key'
  privateKeyPath?: string
  passphrase?: string
  useStoredPassphrase: boolean
  savePassphrase: boolean | undefined
}

interface Props {
  preset?: ConnectionProfile
  onCancel: () => void
  onConnect: (profile: ConnectionProfile, opts: ConnectDialogResult) => void
}

export function ConnectDialog({ preset, onCancel, onConnect }: Props) {
  const [name, setName] = useState(preset?.name ?? '')
  const [host, setHost] = useState(preset?.host ?? '')
  const [port, setPort] = useState(preset ? String(preset.port) : '22')
  const [username, setUsername] = useState(preset?.username ?? '')
  const [authMethod, setAuthMethod] = useState<'password' | 'key'>(preset?.authMethod ?? 'password')
  const [password, setPassword] = useState('')
  const [savePassword, setSavePassword] = useState(true)
  const [privateKeyPath, setPrivateKeyPath] = useState(preset?.privateKeyPath ?? '')
  const [passphrase, setPassphrase] = useState('')
  const [savePassphrase, setSavePassphrase] = useState(true)
  const [save, setSave] = useState(true)
  const [autoReconnect, setAutoReconnect] = useState(true)
  const [err, setErr] = useState<string | null>(null)

  const pickKey = async (): Promise<void> => {
    const p = await window.bastion.pickPrivateKey()
    if (p) setPrivateKeyPath(p)
  }

  const submit = () => {
    const h = host.trim()
    const u = username.trim()
    const p = Number(port)
    if (!h || !u) {
      setErr('请填写主机地址和用户名')
      return
    }
    if (!Number.isInteger(p) || p < 1 || p > 65535) {
      setErr('端口无效（1-65535）')
      return
    }
    if (authMethod === 'key' && !privateKeyPath.trim()) {
      setErr('请选择私钥文件')
      return
    }
    const typedPassword = authMethod === 'password' ? password || undefined : undefined
    const typedPassphrase = authMethod === 'key' ? passphrase || undefined : undefined
    const opts: ConnectDialogResult = {
      password: typedPassword,
      save,
      autoReconnect,
      useStoredPassword: authMethod === 'password' && savePassword && !typedPassword,
      savePassword: authMethod === 'password' ? (savePassword ? (typedPassword ? true : undefined) : false) : undefined,
      authMethod,
      privateKeyPath: authMethod === 'key' ? privateKeyPath.trim() : undefined,
      passphrase: typedPassphrase,
      useStoredPassphrase: authMethod === 'key' && savePassphrase && !typedPassphrase,
      savePassphrase: authMethod === 'key' ? (savePassphrase ? (typedPassphrase ? true : undefined) : false) : undefined
    }
    onConnect(
      {
        id: preset?.id ?? crypto.randomUUID(),
        name: name.trim() || `${u}@${h}`,
        host: h,
        port: p,
        username: u,
        authMethod,
        ...(authMethod === 'key' && privateKeyPath.trim() ? { privateKeyPath: privateKeyPath.trim() } : {})
      },
      opts
    )
  }

  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>{preset ? '连接堡垒机' : '新建连接'}</h2>

        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <div className="modal-body">
            <label className="field">
              名称
              <input type="text" value={name} placeholder="例如：生产堡垒机" onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="field">
              主机地址
              <input type="text" value={host} placeholder="bastion.example.com 或 IP" onChange={(e) => setHost(e.target.value)} />
            </label>
            <div className="row">
              <label className="field">
                端口
                <input type="number" value={port} min={1} max={65535} onChange={(e) => setPort(e.target.value)} />
              </label>
              <label className="field">
                用户名
                <input type="text" value={username} placeholder="SSH 账号" onChange={(e) => setUsername(e.target.value)} />
              </label>
            </div>

            <label className="field">
              认证方式
              <select value={authMethod} onChange={(e) => setAuthMethod(e.target.value as 'password' | 'key')}>
                <option value="password">密码（堡垒机 + MFA）</option>
                <option value="key">私钥（OpenSSH PEM）</option>
              </select>
            </label>

            {authMethod === 'password' ? (
              <>
                <label className="field">
                  密码（可选）
                  <input
                    type="password"
                    value={password}
                    autoComplete="new-password"
                    placeholder={
                      preset?.hasStoredPassword
                        ? '已保存密码（本机加密），留空将使用已存密码，只需输入 MFA'
                        : '留空则认证时按提示输入；MFA 一律在认证时输入'
                    }
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </label>
                <label className="checkbox-field">
                  <input type="checkbox" checked={savePassword} onChange={(e) => setSavePassword(e.target.checked)} />
                  💾 保存密码到本机（Windows 加密存储；MFA 每次仍需输入）
                </label>
                {preset?.hasStoredPassword && !savePassword && (
                  <div className="muted" style={{ fontSize: 12 }}>
                    取消勾选并连接将清除本机已保存的密码
                  </div>
                )}
              </>
            ) : (
              <>
                <label className="field">
                  私钥文件
                  <div className="row" style={{ gap: 8 }}>
                    <input
                      type="text"
                      value={privateKeyPath}
                      placeholder="例如 C:\Users\you\.ssh\id_ed25519"
                      onChange={(e) => setPrivateKeyPath(e.target.value)}
                      readOnly
                    />
                    <button type="button" className="btn" style={{ flex: '0 0 auto' }} onClick={() => void pickKey()}>
                      浏览
                    </button>
                  </div>
                </label>
                <label className="field">
                  密钥口令（可选，无口令留空）
                  <input
                    type="password"
                    value={passphrase}
                    autoComplete="new-password"
                    placeholder={
                      preset?.hasStoredPassphrase
                        ? '已保存口令（本机加密），留空将使用已存口令'
                        : '私钥加密时填写；无口令留空'
                    }
                    onChange={(e) => setPassphrase(e.target.value)}
                  />
                </label>
                <label className="checkbox-field">
                  <input type="checkbox" checked={savePassphrase} onChange={(e) => setSavePassphrase(e.target.checked)} />
                  💾 保存密钥口令到本机（Windows 加密存储）
                </label>
                {preset?.hasStoredPassphrase && !savePassphrase && (
                  <div className="muted" style={{ fontSize: 12 }}>
                    取消勾选并连接将清除本机已保存的密钥口令
                  </div>
                )}
              </>
            )}

            <label className="checkbox-field">
              <input type="checkbox" checked={save} onChange={(e) => setSave(e.target.checked)} />
              保存为连接档案（主机/端口/用户名/认证方式）
            </label>
            <label className="checkbox-field">
              <input type="checkbox" checked={autoReconnect} onChange={(e) => setAutoReconnect(e.target.checked)} />
              掉线自动重连（凭据仅保留在内存，重连时只需重输 MFA）
            </label>
            {err && <div className="error">{err}</div>}
          </div>
          <div className="modal-actions">
            <button type="button" className="btn" onClick={onCancel}>
              取消
            </button>
            <button type="submit" className="btn primary">
              连接
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
