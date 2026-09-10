import { create } from 'zustand'

interface TerminalInfoState {
  /** 各会话终端实时尺寸（cols×rows），底部状态条展示 */
  sizes: Record<string, { cols: number; rows: number }>
  setSize: (sessionId: string, cols: number, rows: number) => void
  clearSize: (sessionId: string) => void
}

/**
 * 终端尺寸注册表：由 TerminalManager 在 fit/resize 时写入，
 * 底部状态条只读展示（窗口放大后能看到实际终端行列数，便于判断布局是否溢出）。
 */
export const useTerminalInfoStore = create<TerminalInfoState>()((set) => ({
  sizes: {},
  setSize: (sessionId, cols, rows) =>
    set((s) => {
      const prev = s.sizes[sessionId]
      if (prev && prev.cols === cols && prev.rows === rows) return s
      return { sizes: { ...s.sizes, [sessionId]: { cols, rows } } }
    }),
  clearSize: (sessionId) =>
    set((s) => {
      if (!(sessionId in s.sizes)) return s
      const next = { ...s.sizes }
      delete next[sessionId]
      return { sizes: next }
    })
}))
