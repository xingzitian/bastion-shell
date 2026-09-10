import { useEffect, useRef, useState } from 'react'
import type { AuthPromptPayload } from '../shared/types'

interface Props {
  prompt: AuthPromptPayload
  onSubmit: (value: string) => void
  onCancel: () => void
}

/**
 * keyboard-interactive 认证应答弹窗。
 * JumpServer/齐治 的密码与 MFA 提示都会依次经这里输入；
 * echo=false 的提示（密码、MFA 验证码）以密码框回显。
 */
export function AuthPromptDialog({ prompt, onSubmit, onCancel }: Props) {
  const [value, setValue] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  return (
    <div className="modal-backdrop">
      <div className="modal auth-modal">
        <h2>SSH 认证</h2>
        {prompt.name && <div className="auth-name">{prompt.name}</div>}
        {prompt.instructions && <div className="auth-instr">{prompt.instructions}</div>}
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (value.length > 0) onSubmit(value)
          }}
        >
          <label className="field">
            <span className="auth-prompt">{prompt.prompt || '请输入应答:'}</span>
            <input
              ref={inputRef}
              type={prompt.echo ? 'text' : 'password'}
              value={value}
              autoComplete="off"
              placeholder={prompt.echo ? '' : '密码 / MFA 验证码（不回显）'}
              onChange={(e) => setValue(e.target.value)}
            />
          </label>
          <div className="modal-actions">
            <button type="button" className="btn" onClick={onCancel}>
              取消认证
            </button>
            <button type="submit" className="btn primary">
              确认
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
