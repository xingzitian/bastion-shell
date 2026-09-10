import type { Terminal, IBufferLine, IDisposable } from '@xterm/xterm'
import type { HighlightColor, HighlightRule } from '../shared/types'

/** 每种颜色只给"命中的关键字字符本身"铺一层浅色底（grep --color 风格），不整行着色 */
export const HIGHLIGHT_COLORS: Record<HighlightColor, { bg: string; fg: string }> = {
  red: { bg: 'rgba(255,99,99,0.32)', fg: '#ff6363' },
  yellow: { bg: 'rgba(255,200,80,0.32)', fg: '#ffc850' },
  green: { bg: 'rgba(80,220,140,0.32)', fg: '#50dc8c' },
  blue: { bg: 'rgba(80,140,255,0.32)', fg: '#508cff' },
  orange: { bg: 'rgba(255,150,60,0.32)', fg: '#ff963c' },
  purple: { bg: 'rgba(190,120,255,0.32)', fg: '#be78ff' }
}

/** 最近扫描的缓冲行数（回滚往上看时会连可视区一起补扫） */
const SCAN_ROWS = 800
/** 最多保留装饰的行数（超出的丢弃最旧，长期会话内存有界） */
const MAX_TRACKED_LINES = 4000
const DEBOUNCE_MS = 120

/**
 * 字符单元格宽度（xterm 网格对齐用）：
 * 中文/全角/emoji 等宽字符占 2 格，组合附标占 0 格，其余 1 格。
 * 没有这个换算，含中文的堡垒机菜单里装饰位置会整体漂移。
 */
export function charWidth(ch: string): number {
  const c = ch.codePointAt(0) ?? 0
  if (c === 0) return 0
  if ((c >= 0x0300 && c <= 0x036f) || (c >= 0x20d0 && c <= 0x20ff) || (c >= 0xfe00 && c <= 0xfe0f)) return 0
  if (
    (c >= 0x1100 && c <= 0x115f) ||
    (c >= 0x2e80 && c <= 0x303e) ||
    (c >= 0x3041 && c <= 0x33ff) ||
    (c >= 0x3400 && c <= 0x4dbf) ||
    (c >= 0x4e00 && c <= 0x9fff) ||
    (c >= 0xa000 && c <= 0xa4cf) ||
    (c >= 0xac00 && c <= 0xd7a3) ||
    (c >= 0xf900 && c <= 0xfaff) ||
    (c >= 0xfe30 && c <= 0xfe4f) ||
    (c >= 0xff00 && c <= 0xff60) ||
    (c >= 0xffe0 && c <= 0xffe6) ||
    (c >= 0x1f300 && c <= 0x1faff) ||
    (c >= 0x20000 && c <= 0x3fffd)
  ) {
    return 2
  }
  return 1
}

export function textWidth(s: string): number {
  let w = 0
  for (const ch of s) w += charWidth(ch)
  return w
}

/**
 * 高亮渲染总开关（临时关闭）。
 * 关闭原因：连续输出的日志场景下，装饰"滞后补算 + 行滚动重排"会产生
 * 错位/不实时/跳动感；窗口 resize 的整轮重建也会引起放大卡顿。
 * 规则数据与 🖍 管理弹窗保留（修改仍会保存），待换用更稳的渲染方案后重新开启。
 */
export const HIGHLIGHT_RENDER_ENABLED = false

/**
 * 关键词高亮引擎 v3：只给关键字字符本身铺浅色底。
 * - marker 偏移严格相对光标行（viewportY + cursorY），滚动回看不再漂移；
 * - 逻辑行跨折行拼接后匹配，命中位置按单元格宽度换算并跨行切成装饰段；
 * - 中文/全角字符按 2 格宽度对齐；
 * - 不做整行底色、不用滚动条彩色标尺（满屏色块是"眼睛疼"的根源）；
 * - 窗口 resize 后整轮重建，行内容变化按行去重重绘。
 */
export class HighlightEngine {
  private lines = new Map<IBufferLine, { text: string; decorations: IDisposable[] }>()
  private timer: ReturnType<typeof setTimeout> | null = null
  private disposed = false
  private compiled: Array<{ regex: RegExp; color: HighlightColor }> = []
  private writeSub: IDisposable | null = null
  private resizeSub: IDisposable | null = null
  private scrollSub: IDisposable | null = null

  constructor(private readonly term: Terminal, rules: HighlightRule[]) {
    if (!HIGHLIGHT_RENDER_ENABLED) return // 临时关闭：不订阅任何事件、不产生任何装饰
    this.setRules(rules)
    this.writeSub = term.onWriteParsed(() => this.schedule())
    // resize 后 xterm 重排折行，旧装饰位置失效 → 整轮重建
    this.resizeSub = term.onResize(() => this.rebuild())
    // 回滚查看时把可视区也纳入扫描
    this.scrollSub = term.onScroll(() => this.schedule())
  }

  setRules(rules: HighlightRule[]): void {
    if (!HIGHLIGHT_RENDER_ENABLED) return
    this.compiled = rules
      .filter((r) => r.enabled && r.pattern.trim())
      .map((r) => {
        try {
          // 默认忽略大小写（日志中 Failed/WARNING 混排常见）；规则可显式关闭
          const flags = r.ignoreCase === false ? 'g' : 'gi'
          return { regex: new RegExp(r.pattern, flags), color: r.color }
        } catch {
          return null
        }
      })
      .filter((x): x is { regex: RegExp; color: HighlightColor } => x !== null)
    this.rebuild()
  }

  private schedule(): void {
    if (this.disposed) return
    if (this.timer) clearTimeout(this.timer)
    this.timer = setTimeout(() => {
      this.timer = null
      this.scan()
    }, DEBOUNCE_MS)
  }

  private rebuild(): void {
    if (this.disposed) return
    this.clearAll()
    this.schedule()
  }

  private clearAll(): void {
    for (const e of this.lines.values()) {
      for (const d of e.decorations) {
        try {
          d.dispose()
        } catch {
          /* ignore */
        }
      }
    }
    this.lines.clear()
  }

  private scan(): void {
    if (this.disposed || this.compiled.length === 0) return
    const buffer = this.term.buffer.active
    const cols = this.term.cols || 80
    const end = buffer.length - 1
    const start = Math.max(0, end - SCAN_ROWS)
    // 可视区（回滚查看时可能在窗口之上）
    const visStart = Math.min(buffer.viewportY, end)
    const visEnd = Math.min(buffer.viewportY + this.term.rows - 1, end)

    for (let row = start; row <= end; row++) {
      const line = buffer.getLine(row)
      if (!line || line.isWrapped) continue // isWrapped 的行属于上一逻辑行
      this.scanLogicalRow(buffer, line, row, end, cols)
    }
    if (visStart < start) {
      const extraEnd = Math.min(start - 1, visEnd)
      for (let row = visStart; row <= extraEnd; row++) {
        const line = buffer.getLine(row)
        if (!line || line.isWrapped) continue
        this.scanLogicalRow(buffer, line, row, extraEnd, cols)
      }
    }
  }

  /** 扫描一条逻辑行（含其折行延续）：按行对象 + 文本去重，内容变了才重绘 */
  private scanLogicalRow(buffer: Terminal['buffer']['active'], firstLine: IBufferLine, firstRow: number, maxRow: number, cols: number): void {
    let text = firstLine.translateToString(true)
    let totalRows = 1
    while (firstRow + totalRows <= maxRow) {
      const next = buffer.getLine(firstRow + totalRows)
      if (!next || !next.isWrapped) break
      text += next.translateToString(true)
      totalRows++
    }
    const prev = this.lines.get(firstLine)
    if (prev && prev.text === text) return
    if (prev) {
      for (const d of prev.decorations) {
        try {
          d.dispose()
        } catch {
          /* ignore */
        }
      }
    }

    const decorations: IDisposable[] = []
    if (text) {
      for (const { regex, color } of this.compiled) {
        regex.lastIndex = 0
        let m: RegExpExecArray | null
        while ((m = regex.exec(text)) !== null) {
          if (m[0].length === 0) {
            regex.lastIndex++
            continue
          }
          this.emitMatch(buffer, firstRow, text, m.index, m[0], cols, color, decorations)
        }
      }
    }
    this.lines.set(firstLine, { text, decorations })
    while (this.lines.size > MAX_TRACKED_LINES) {
      const first = this.lines.keys().next().value
      if (first === undefined) break
      const e = this.lines.get(first)
      if (e) {
        for (const d of e.decorations) {
          try {
            d.dispose()
          } catch {
            /* ignore */
          }
        }
      }
      this.lines.delete(first)
    }
  }

  /** 把一处命中的文本按单元格宽度换算位置，跨折行切成多段装饰 */
  private emitMatch(
    buffer: Terminal['buffer']['active'],
    baseRow: number,
    fullText: string,
    matchIndex: number,
    matchText: string,
    cols: number,
    color: HighlightColor,
    out: IDisposable[]
  ): void {
    const prefixW = textWidth(fullText.slice(0, matchIndex))
    let row = baseRow + Math.floor(prefixW / cols)
    let x = prefixW % cols
    let segStart = -1
    let segWidth = 0

    const flush = (): void => {
      if (segStart < 0) return
      const line = buffer.getLine(row)
      if (line && row >= 0 && row < buffer.length) {
        try {
          const marker = this.term.registerMarker(row - (buffer.viewportY + buffer.cursorY))
          const deco = this.term.registerDecoration({
            marker,
            x: segStart,
            width: segWidth,
            backgroundColor: HIGHLIGHT_COLORS[color].bg,
            layer: 'bottom'
          })
          if (deco) out.push(deco)
        } catch {
          /* 单段失败不中断 */
        }
      }
      segStart = -1
      segWidth = 0
    }

    for (const ch of matchText) {
      const w = charWidth(ch)
      // 该字符在剩余单元格放不下（宽字符不会劈半）：换到下一行
      if (x + w > cols) {
        flush()
        row++
        x = 0
      }
      if (segStart < 0) segStart = x
      segWidth += w
      x += w
    }
    flush()
  }

  dispose(): void {
    this.disposed = true
    if (this.timer) clearTimeout(this.timer)
    this.writeSub?.dispose()
    this.resizeSub?.dispose()
    this.scrollSub?.dispose()
    this.clearAll()
  }
}
