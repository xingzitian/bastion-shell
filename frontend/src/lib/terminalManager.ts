import { Terminal, IDisposable } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { HighlightEngine } from './highlight'
import { parseOsc52, decodeOsc52Base64 } from './osc52'
import { toXtermTheme } from './theme'
import { bus } from './bus'
import { analyzePaste, mergeContinuation, getRememberedPasteChoice, PasteChoice } from './smartPaste'
import { useTerminalInfoStore } from '../store/terminalInfo'
import type { HighlightRule, TerminalTheme } from '../shared/types'

export interface PasteAsk {
  text: string
  lines: number
}

export interface TerminalConfig {
  broadcastTargets: string[]
  highlightRules: HighlightRule[]
  theme: TerminalTheme
  /** 多行粘贴需要选择时回调（由 React 层弹窗） */
  onPasteAsk: (ask: PasteAsk) => void
}

interface TerminalEntry {
  sessionId: string
  terminal: Terminal
  fit: FitAddon
  highlight: HighlightEngine
  element: HTMLDivElement
  container: HTMLElement | null
  config: TerminalConfig
  disposables: IDisposable[]
  cleanupFns: Array<() => void>
  disposed: boolean
  /** 会话已关闭（冻结画面）：不再收发数据，但实例与缓冲区保留 */
  closed: boolean
  /** 防"粘贴两遍"：上一次粘贴的文本与时间戳（双触发/按键重复去重） */
  lastPasteText: string
  lastPasteAt: number
  /** 防 ResizeObserver 循环：最近一次 fit 时的容器像素尺寸（未变则不重复 fit） */
  lastFitW: number
  lastFitH: number
  /** OSC 52 剪贴板桥接监听（随终端实例存亡，不随会话换绑拆除） */
  osc52: IDisposable | null
}

/**
 * 终端单例管理器：每个会话的 xterm 实例**只创建一次、永不因界面搬动而销毁**。
 * 标签切换 / 跨组移动只是把同一个 DOM 节点 attach/detach 到不同容器，
 * 缓冲区与滚动历史天然全程保留——数据丢失在构造上不可能发生。
 */
class TerminalManager {
  private entries = new Map<string, TerminalEntry>()

  getOrCreate(sessionId: string, config: TerminalConfig): TerminalEntry {
    const existing = this.entries.get(sessionId)
    if (existing) return existing

    const element = document.createElement('div')
    element.className = 'terminal'

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'Consolas, "Cascadia Mono", "Courier New", monospace',
      scrollback: 10000,
      // 高亮引擎使用 registerMarker/registerDecoration（提案 API），必须开启
      allowProposedApi: true,
      // 禁用 xterm 内置括号粘贴包裹（堡垒机菜单声明支持却不解析，包裹会产生 0~/1~ 垃圾）
      ignoreBracketedPasteMode: true,
      // 浏览器版：选中即复制（MobaXterm/Putty 风格），规避 Ctrl+Shift+C 被 Edge 占用为审查元素
      copyOnSelect: true,
      theme: toXtermTheme(config.theme)
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.loadAddon(
      new WebLinksAddon((_event, uri) => {
        void window.bastion.openExternal(uri)
      })
    )
    // OSC 52 剪贴板桥接：远端 vim 里 yy 复制 → 直接进 Windows 剪贴板（配 vim-oscyank）
    const osc52 = terminal.parser.registerOscHandler(52, (data) => {
      const p = parseOsc52(data)
      if (!p) return false
      if (p.selection !== 'c' && p.selection !== 'p' && p.selection !== '') return false
      const text = decodeOsc52Base64(p.base64)
      if (text) {
        try {
          window.bastion.writeClipboard(text)
          bus.emit('ui:toast', { kind: 'info', text: `已从远端复制 ${text.length} 个字符到剪贴板` })
        } catch {
          /* ignore */
        }
      }
      return true
    })
    terminal.open(element)
    const highlight = new HighlightEngine(terminal, config.highlightRules)

    const entry: TerminalEntry = {
      sessionId,
      terminal,
      fit,
      highlight,
      element,
      container: null,
      config,
      disposables: [],
      cleanupFns: [],
      disposed: false,
      closed: false,
      lastPasteText: '',
      lastPasteAt: 0,
      lastFitW: 0,
      lastFitH: 0,
      osc52
    }
    this.entries.set(sessionId, entry)
    this.wireInput(entry)
    this.wireOutput(entry)
    this.wireResize(entry)
    return entry
  }

  attach(sessionId: string, container: HTMLElement, config: TerminalConfig): void {
    try {
      this.attachUnsafe(sessionId, container, config)
    } catch (err) {
      console.error('[terminalManager] attach failed:', err)
      try {
        window.bastion.logToMain('error', `terminal attach failed: ${err instanceof Error ? err.message : String(err)}`)
      } catch {
        /* ignore */
      }
    }
  }

  private attachUnsafe(sessionId: string, container: HTMLElement, config: TerminalConfig): void {
    const entry = this.getOrCreate(sessionId, config)
    entry.config = config
    this.applyConfig(entry)
    if (entry.container === container) return
    this.detach(sessionId)
    container.appendChild(entry.element)
    entry.container = container
    // 入 DOM 后再 fit：双 rAF 等分屏/挂载后的布局彻底稳定再量尺寸，
    // 避免一次性 fit 读到过渡态尺寸造成"压缩事故"
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (entry.disposed || entry.container !== container) return
        this.fitEntry(entry)
      })
    })
  }

  detach(sessionId: string): void {
    const entry = this.entries.get(sessionId)
    if (!entry || !entry.container) return
    if (entry.element.parentElement === entry.container) {
      entry.container.removeChild(entry.element)
    }
    entry.container = null
  }

  setConfig(sessionId: string, patch: Partial<TerminalConfig>): void {
    const entry = this.entries.get(sessionId)
    if (!entry) return
    entry.config = { ...entry.config, ...patch }
    this.applyConfig(entry)
  }

  private applyConfig(entry: TerminalEntry): void {
    entry.terminal.options.theme = toXtermTheme(entry.config.theme)
    entry.highlight.setRules(entry.config.highlightRules)
  }

  focus(sessionId: string): void {
    this.entries.get(sessionId)?.terminal.focus()
  }

  /** 执行一次 fit 并同步尺寸到 store/远端，同时刷新去重基准（供强制刷新复用） */
  private fitEntry(entry: TerminalEntry): void {
    if (entry.disposed || !entry.container) return
    try {
      entry.fit.fit()
    } catch {
      return
    }
    entry.lastFitW = entry.element.clientWidth
    entry.lastFitH = entry.element.clientHeight
    useTerminalInfoStore.getState().setSize(entry.sessionId, entry.terminal.cols, entry.terminal.rows)
    if (!entry.closed) {
      window.bastion.resizeSession(entry.sessionId, entry.terminal.cols, entry.terminal.rows)
    }
  }

  resizeNow(sessionId: string): void {
    const entry = this.entries.get(sessionId)
    if (entry) this.fitEntry(entry)
  }

  /** 强制刷新所有存活终端尺寸（顶部状态栏「⟳ 刷新」按钮） */
  fitAll(): void {
    for (const entry of this.entries.values()) this.fitEntry(entry)
  }

  pasteAs(sessionId: string, mode: PasteChoice, text: string): void {
    const entry = this.entries.get(sessionId)
    if (!entry) return
    this.sendInput(entry, mode === 'raw' ? text : mergeContinuation(text))
    bus.emit('ui:toast', { kind: 'info', text: mode === 'raw' ? '已原样粘贴多行内容' : '已合并为单行粘贴' })
  }

  disposeSession(sessionId: string): void {
    const entry = this.entries.get(sessionId)
    if (!entry) return
    entry.disposed = true
    this.detach(sessionId)
    for (const d of entry.disposables) {
      try {
        d.dispose()
      } catch {
        /* ignore */
      }
    }
    for (const fn of entry.cleanupFns) {
      try {
        fn()
      } catch {
        /* ignore */
      }
    }
    entry.highlight.dispose()
    entry.osc52?.dispose()
    entry.terminal.dispose()
    useTerminalInfoStore.getState().clearSize(sessionId)
    this.entries.delete(sessionId)
  }

  /** 拆除会话接线（输入/输出/尺寸监听），保留 terminal 实例与全部历史缓冲 */
  private unwire(entry: TerminalEntry): void {
    for (const d of entry.disposables) {
      try {
        d.dispose()
      } catch {
        /* ignore */
      }
    }
    entry.disposables = []
    for (const fn of entry.cleanupFns) {
      try {
        fn()
      } catch {
        /* ignore */
      }
    }
    entry.cleanupFns = []
  }

  /**
   * 窗口复用（原窗口重开）：把冻结的终端实例换绑到一个新会话——
   * 历史滚动内容原样保留，写入分隔线后继续追加新会话输出。
   * 标签/分屏中的 sessionId 由调用方同步换绑。
   */
  rebindSession(oldSessionId: string, newSessionId: string): void {
    const entry = this.entries.get(oldSessionId)
    if (!entry || entry.disposed) return
    this.unwire(entry)
    this.entries.delete(oldSessionId)
    entry.sessionId = newSessionId
    entry.closed = false
    entry.lastPasteText = ''
    entry.lastPasteAt = 0
    entry.lastFitW = 0
    entry.lastFitH = 0
    this.entries.set(newSessionId, entry)
    this.wireInput(entry)
    this.wireOutput(entry)
    this.wireResize(entry)
    entry.terminal.write('\r\n\x1b[36m═══ 🔌 已在本窗口重开新会话（历史记录保留）═══\x1b[0m\r\n')
    if (!entry.disposed) {
      window.bastion.resizeSession(newSessionId, entry.terminal.cols, entry.terminal.rows)
    }
  }

  // ---- 输入（键盘/广播/复制粘贴） ----
  private wireInput(entry: TerminalEntry): void {
    const { sessionId, terminal } = entry

    const inputDisposable = terminal.onData((d) => this.sendInput(entry, d))
    entry.disposables.push(inputDisposable)

    terminal.attachCustomKeyEventHandler((e) => {
      if (e.type === 'keydown' && e.ctrlKey && !e.metaKey) {
        const k = e.key.toLowerCase()
        // 复制：Ctrl+Shift+C（Electron）或 Ctrl+Alt+C（浏览器，避开 Edge 审查元素 F12 冲突）
        const isCopy = (e.shiftKey && !e.altKey && k === 'c') || (!e.shiftKey && e.altKey && k === 'c')
        if (isCopy) {
          if (terminal.hasSelection()) {
            const text = terminal.getSelection()
            void navigator.clipboard.writeText(text).then(() => {
              bus.emit('ui:toast', { kind: 'success', text: `已复制 ${text.length} 个字符` })
            })
          }
          return false
        }
        if (e.shiftKey && !e.altKey && k === 'v') {
          // e.repeat：长按组合键产生的重复 keydown 只处理第一次（防"粘贴两遍"）
          if (!e.repeat) void this.doPaste(entry)
          return false
        }
      }
      return true
    })

    let shiftHeld = false
    const trackKeyDown = (e: KeyboardEvent): void => {
      shiftHeld = e.shiftKey
    }
    const trackKeyUp = (e: KeyboardEvent): void => {
      shiftHeld = e.shiftKey
    }
    window.addEventListener('keydown', trackKeyDown)
    window.addEventListener('keyup', trackKeyUp)
    const pasteHandler = (ev: Event): void => {
      const ce = ev as ClipboardEvent
      const t = ce.clipboardData?.getData('text') ?? ''
      if (t) {
        ev.preventDefault()
        // stopImmediatePropagation：同元素同阶段的其他监听器也一并截断
        ev.stopImmediatePropagation()
        if (this.acceptPaste(entry, t)) this.pasteText(entry, t, shiftHeld)
      }
    }
    entry.element.addEventListener('paste', pasteHandler, true)
    entry.cleanupFns.push(() => {
      window.removeEventListener('keydown', trackKeyDown)
      window.removeEventListener('keyup', trackKeyUp)
      entry.element.removeEventListener('paste', pasteHandler, true)
    })
  }

  private sendInput(entry: TerminalEntry, data: string): void {
    if (entry.closed || entry.disposed) return
    const targets = entry.config.broadcastTargets
    if (targets.includes(entry.sessionId) && targets.length > 1) {
      for (const sid of targets) window.bastion.writeSession(sid, data)
    } else {
      window.bastion.writeSession(entry.sessionId, data)
    }
  }

  private async doPaste(entry: TerminalEntry): Promise<void> {
    let text = ''
    try {
      text = await window.bastion.readClipboard()
    } catch {
      text = ''
    }
    if (!text) {
      // 兜底：preload 的 clipboard 模块不可用时走渲染进程 Clipboard API
      try {
        text = await navigator.clipboard.readText()
      } catch {
        text = ''
      }
    }
    if (text && this.acceptPaste(entry, text)) this.pasteText(entry, text, false)
  }

  /**
   * 防"粘贴两遍"：同一文本在 300ms 内的重复粘贴视为
   * 双事件触发（菜单加速键 + 原生 paste）或组合键重复，只放行第一次。
   */
  private acceptPaste(entry: TerminalEntry, text: string): boolean {
    const now = Date.now()
    if (text === entry.lastPasteText && now - entry.lastPasteAt < 300) return false
    entry.lastPasteText = text
    entry.lastPasteAt = now
    return true
  }

  private pasteText(entry: TerminalEntry, raw: string, forceAsk: boolean): void {
    const cleaned = raw.replace(/\x1b\[200~/g, '').replace(/\x1b\[201~/g, '')
    if (!/[\r\n]/.test(cleaned)) {
      this.sendInput(entry, cleaned)
      return
    }
    const a = analyzePaste(cleaned)
    if (a.isContinuation) {
      this.sendInput(entry, mergeContinuation(cleaned))
      bus.emit('ui:toast', { kind: 'info', text: `已识别续行命令，合并为单行发送（${a.lines} 行 → 1 行）` })
      return
    }
    if (!forceAsk) {
      const remembered = getRememberedPasteChoice()
      if (remembered) {
        this.pasteAs(entry.sessionId, remembered, cleaned)
        return
      }
    }
    entry.config.onPasteAsk({ text: cleaned, lines: a.lines })
  }

  // ---- 输出（数据流 + 会话关闭） ----
  private wireOutput(entry: TerminalEntry): void {
    const { sessionId, terminal } = entry
    const offData = window.bastion.onSessionData(({ sessionId: sid, data }) => {
      if (sid === sessionId && !entry.disposed && !entry.closed) terminal.write(data)
    })
    const offClosed = window.bastion.onSessionClosed(({ sessionId: sid, code }) => {
      if (sid !== sessionId || entry.closed || entry.disposed) return
      // 冻结画面：写入醒目标记后保留终端实例与全部滚动历史，
      // 用户回来后能看到离开前最后的内容和会话结束原因。
      entry.closed = true
      terminal.write(`\r\n\x1b[33m⛔ [会话已关闭${code !== undefined ? `，退出码 ${code}` : ''}] 画面已冻结，可回看历史；如需重连请新建会话\x1b[0m\r\n`)
      offData()
      offClosed()
    })
    entry.cleanupFns.push(() => {
      offData()
      offClosed()
    })
  }

  // ---- 尺寸自适应 ----
  private wireResize(entry: TerminalEntry): void {
    let raf = 0
    const observer = new ResizeObserver(() => {
      if (entry.disposed || !entry.container) return
      // 合并到下一帧：窗口/分隔条一次变化只 fit 一次，杜绝
      // "ResizeObserver loop completed with undelivered notifications" 连锁抖动
      if (raf) return
      raf = requestAnimationFrame(() => {
        raf = 0
        if (entry.disposed || !entry.container) return
        const w = entry.element.clientWidth
        const h = entry.element.clientHeight
        // 跳过 0×0 / display:none 的伪触发，避免把 lastFit 记成 0 造成后续误判
        if (w <= 0 || h <= 0) return
        // 像素尺寸没变（<1px）就不 fit——切断 fit→布局变化→再触发观察的循环
        if (Math.abs(w - entry.lastFitW) < 1 && Math.abs(h - entry.lastFitH) < 1) return
        this.fitEntry(entry)
      })
    })
    observer.observe(entry.element)
    entry.cleanupFns.push(() => {
      observer.disconnect()
      if (raf) cancelAnimationFrame(raf)
    })
  }
}

let managerInstance: TerminalManager | null = null

export function getTerminalManager(): TerminalManager {
  if (!managerInstance) managerInstance = new TerminalManager()
  return managerInstance
}
