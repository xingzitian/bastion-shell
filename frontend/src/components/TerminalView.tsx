import { useEffect, useRef, useState } from 'react'
import { getTerminalManager, PasteAsk } from '../lib/terminalManager'
import { setRememberedPasteChoice, PasteChoice } from '../lib/smartPaste'
import { useUploadStore } from '../store/upload'
import { bus } from '../lib/bus'
import { PasteDialog } from './PasteDialog'
import type { HighlightRule, TerminalTheme } from '../shared/types'

interface Props {
  sessionId: string
  active: boolean
  /** 广播输入目标集合（含本会话时，输入将发往集合内全部会话） */
  broadcastTargets: string[]
  highlightRules: HighlightRule[]
  theme: TerminalTheme
}

/**
 * 终端面板薄壳：真正的 xterm 实例由 TerminalManager 单例持有，
 * 本组件只负责把该会话的 DOM 节点 attach/detach 到当前位置。
 * 标签切换 / 跨组移动不销毁终端 → 缓冲区与滚动历史全程保留。
 */
export function TerminalView({ sessionId, active, broadcastTargets, highlightRules, theme }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const [pasteAsk, setPasteAsk] = useState<PasteAsk | null>(null)

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const mgr = getTerminalManager()
    mgr.attach(sessionId, host, {
      broadcastTargets,
      highlightRules,
      theme,
      onPasteAsk: setPasteAsk
    })
    return () => {
      mgr.detach(sessionId)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId])

  // 配置变更（广播目标/高亮规则/主题）→ 管理器热更新
  useEffect(() => {
    getTerminalManager().setConfig(sessionId, { broadcastTargets, highlightRules, theme })
  }, [sessionId, broadcastTargets, highlightRules, theme])

  // 变为可见：重新 fit + 聚焦
  useEffect(() => {
    if (!active) return
    const t = setTimeout(() => {
      getTerminalManager().resizeNow(sessionId)
      getTerminalManager().focus(sessionId)
    }, 0)
    return () => clearTimeout(t)
  }, [active, sessionId])

  const choosePaste = (mode: PasteChoice, remember: boolean): void => {
    const ask = pasteAsk
    if (!ask) return
    if (remember) setRememberedPasteChoice(mode)
    getTerminalManager().pasteAs(sessionId, mode, ask.text)
    setPasteAsk(null)
    // 弹窗关闭后把焦点还给终端，粘贴完即可直接回车
    getTerminalManager().focus(sessionId)
  }

  // 拖拽：文件夹 → rsync 增量同步；文件 → rz 上传
  const onDragOver = (e: React.DragEvent): void => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }
  const onDrop = (e: React.DragEvent): void => {
    e.preventDefault()
    const files = Array.from(e.dataTransfer.files)
    if (files.length === 0) return
    const paths: string[] = []
    for (const f of files) {
      try {
        const p = window.bastion.getPathForFile(f)
        if (p) paths.push(p)
      } catch {
        /* 非本地文件忽略 */
      }
    }
    if (paths.length === 0) return
    void (async () => {
      let isDir = false
      try {
        isDir = (await window.bastion.statPath(paths[0])).isDir
      } catch {
        isDir = false
      }
      if (isDir) {
        // 文件夹 → rsync 增量同步（实验性；rz 不支持目录）
        try {
          const r = await window.bastion.syncDirectory({
            sessionId,
            localPath: paths[0],
            remotePath: '.',
            direction: 'upload'
          })
          bus.emit('ui:toast', { kind: r.ok ? 'success' : 'error', text: r.message })
        } catch (err) {
          bus.emit('ui:toast', { kind: 'error', text: err instanceof Error ? err.message : String(err) })
        }
        return
      }
      // 文件 → rz 上传（稳定可靠，含哈希跳过：没变不传，有进度面板）
      void window.bastion.sendFiles(sessionId, paths, useUploadStore.getState().overwrite).catch(() => {
        /* 错误经 zmodem:event 提示 */
      })
    })()
  }

  return (
    <>
      <div ref={hostRef} style={{ flex: 1, display: 'flex', minWidth: 0, minHeight: 0 }} onDragOver={onDragOver} onDrop={onDrop} />
      {pasteAsk && (
        <PasteDialog
          text={pasteAsk.text}
          lines={pasteAsk.lines}
          onChoose={choosePaste}
          onCancel={() => {
            setPasteAsk(null)
            getTerminalManager().focus(sessionId)
          }}
        />
      )}
    </>
  )
}
