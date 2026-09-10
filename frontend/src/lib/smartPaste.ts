/** 智能粘贴：续行式多行命令识别与合并（纯逻辑，可单测） */

export type PasteChoice = 'raw' | 'merged'

const REMEMBER_KEY = 'bastion-paste-mode'

export interface PasteAnalysis {
  lines: number
  /** 除末行外每行都以 \ 结尾 → 判定为单条续行命令 */
  isContinuation: boolean
}

export function analyzePaste(text: string): PasteAnalysis {
  const lines = text.split(/\r\n|\n|\r/)
  const nonEmpty = lines.filter((l) => l.trim().length > 0)
  const isContinuation =
    nonEmpty.length > 1 && nonEmpty.slice(0, -1).every((l) => l.replace(/\s+$/, '').endsWith('\\'))
  return { lines: lines.length, isContinuation }
}

/** 把续行式多行命令合并为单行：去掉行尾 \ 与行首/行尾空白，行间以单个空格连接 */
export function mergeContinuation(text: string): string {
  const lines = text.split(/\r\n|\n|\r/)
  let out = ''
  for (const line of lines) {
    const t = line.trim()
    if (!t) continue
    const part = t.endsWith('\\') ? t.slice(0, -1).trim() : t
    if (!part) continue
    if (out && !out.endsWith(' ')) out += ' '
    out += part
  }
  return out.trim()
}

export function getRememberedPasteChoice(): PasteChoice | null {
  try {
    const v = localStorage.getItem(REMEMBER_KEY)
    return v === 'raw' || v === 'merged' ? v : null
  } catch {
    return null
  }
}

export function setRememberedPasteChoice(c: PasteChoice): void {
  try {
    localStorage.setItem(REMEMBER_KEY, c)
  } catch {
    /* ignore */
  }
}
