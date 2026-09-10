import { create } from 'zustand'
import { bus } from '../lib/bus'

interface BroadcastState {
  ids: string[]
  master: boolean
  /** 标签 📡 开关：进出子集 */
  toggle: (sessionId: string) => void
  toggleMaster: () => void
  selectAll: (allIds: string[]) => void
  clear: () => void
  /** 会话关闭时从子集移除 */
  prune: (sessionId: string) => void
  add: (sessionId: string) => void
  setMaster: (v: boolean) => void
}

export const useBroadcastStore = create<BroadcastState>()((set) => ({
  ids: [],
  master: false,

  toggle: (sessionId) =>
    set((s) => {
      if (s.ids.includes(sessionId)) {
        const next = s.ids.filter((x) => x !== sessionId)
        bus.emit('ui:toast', { kind: 'info', text: `已退出广播子集（当前 ${next.length} 个会话）` })
        return { ids: next }
      }
      bus.emit('ui:toast', {
        kind: 'info',
        text: `已加入广播子集（当前 ${s.ids.length + 1} 个会话）—— 总开关打开时输入将发往全部 📡 会话`
      })
      return { ids: [...s.ids, sessionId] }
    }),

  toggleMaster: () =>
    set((s) => {
      const nv = !s.master
      bus.emit('ui:toast', {
        kind: 'info',
        text: nv
          ? s.ids.length > 0
            ? `📡 广播已开启：输入将发送到 ${s.ids.length} 个标记会话`
            : '📡 广播已开启：还没有标记目标——先点会话标签上的 📡'
          : '📡 广播已关闭：恢复单会话输入'
      })
      return { master: nv }
    }),

  selectAll: (allIds) => {
    set({ ids: allIds })
    bus.emit('ui:toast', { kind: 'info', text: `已全选 ${allIds.length} 个会话加入广播子集` })
  },

  clear: () => {
    set({ ids: [] })
    bus.emit('ui:toast', { kind: 'info', text: '已清空广播子集' })
  },

  prune: (sessionId) => set((s) => ({ ids: s.ids.filter((x) => x !== sessionId) })),

  add: (sessionId) => set((s) => (s.ids.includes(sessionId) ? s : { ids: [...s.ids, sessionId] })),

  setMaster: (v) => set({ master: v })
}))
