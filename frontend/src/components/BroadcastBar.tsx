import { useEffect, useRef, useState } from 'react'
import { bus } from '../lib/bus'
import { analyzePaste, mergeContinuation } from '../lib/smartPaste'

interface Props {
  visible: boolean
  targets: string[]
}

/**
 * Xshell 式群发输入栏（Compose Bar）：
 * 专用输入框，编辑键（退格/Tab/方向键）在框内原生处理，
 * 每个按键实时转发到所有 📡 目标会话——多台机器同时 Tab 补全、
 * 同时退格修正；回车执行并清空，↑↓ 翻历史，Ctrl+C 群发中断。
 */
export function BroadcastBar({ visible, targets }: Props) {
  const [value, setValue] = useState('')
  const [history, setHistory] = useState<string[]>([])
  const [histIdx, setHistIdx] = useState(-1)
  const inputRef = useRef<HTMLInputElement>(null)
  const composingRef = useRef(false)

  useEffect(() => {
    if (visible) inputRef.current?.focus()
  }, [visible])

  const sendToTargets = (data: string): void => {
    if (!data) return
    for (const sid of targets) window.bastion.writeSession(sid, data)
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>): void => {
    if (e.key === 'Enter') {
      if (composingRef.current) return
      e.preventDefault()
      const cmd = value
      if (cmd.trim()) setHistory((h) => [...h.slice(-49), cmd])
      setHistIdx(-1)
      setValue('')
      sendToTargets('\r')
      return
    }
    if (e.key === 'Tab') {
      e.preventDefault()
      sendToTargets('\t')
      return
    }
    if (e.key === 'Backspace') {
      e.preventDefault()
      setValue((v) => v.slice(0, -1))
      sendToTargets('\x7f')
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHistIdx((i) => {
        const next = i < 0 ? history.length - 1 : Math.max(0, i - 1)
        if (history[next] !== undefined) setValue(history[next])
        return next
      })
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHistIdx((i) => {
        const next = i < 0 ? -1 : Math.min(history.length - 1, i + 1)
        if (next < 0) setValue('')
        else if (history[next] !== undefined) setValue(history[next])
        return next
      })
      return
    }
    if (e.ctrlKey && (e.key === 'c' || e.key === 'C')) {
      e.preventDefault()
      setValue('')
      setHistIdx(-1)
      sendToTargets('\x03')
      return
    }
    if (e.ctrlKey || e.metaKey || e.altKey) return
    if (e.key.length === 1) {
      e.preventDefault()
      setValue((v) => v + e.key)
      sendToTargets(e.key)
    }
  }

  const onPaste = (e: React.ClipboardEvent<HTMLInputElement>): void => {
    e.preventDefault()
    const t = e.clipboardData.getData('text')
    if (!t) return
    const a = analyzePaste(t)
    if (a.isContinuation) {
      const merged = mergeContinuation(t)
      setValue((v) => v + merged)
      sendToTargets(merged)
      bus.emit('ui:toast', { kind: 'info', text: `已识别续行命令，合并为单行群发（${a.lines} 行 → 1 行）` })
      return
    }
    setValue((v) => v + t)
    sendToTargets(t)
  }

  return (
    <div className={`broadcast-bar ${visible ? 'visible' : ''}`}>
      <span className="bcast-bar-label">📡 群发 {targets.length} 台</span>
      <input
        ref={inputRef}
        className="bcast-bar-input"
        value={value}
        disabled={targets.length === 0}
        placeholder={
          targets.length === 0
            ? '先在会话标签上点 📡 标记目标机器'
            : '实时群发输入栏：Tab 补全 / 退格 / ↑↓ 历史 / 回车执行并清空 / Ctrl+C 中断'
        }
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        onCompositionStart={() => {
          composingRef.current = true
        }}
        onCompositionEnd={(e) => {
          composingRef.current = false
          const t = (e as unknown as CompositionEvent).data
          if (t) {
            setValue((v) => v + t)
            sendToTargets(t)
          }
        }}
      />
      <button
        className="mini-btn"
        title="清空输入框（不发送）"
        onClick={() => {
          setValue('')
          setHistIdx(-1)
        }}
      >
        清空
      </button>
    </div>
  )
}
