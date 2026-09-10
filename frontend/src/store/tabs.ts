import { create } from 'zustand'
import { useSplitStore } from './split'
import { useBroadcastStore } from './broadcast'
import { findGroupOfSession } from '../lib/groupTree'
import type { BastionApi } from '../shared/types'

export interface Tab {
  sessionId: string
  connectionId: string
  title: string
  /** 全局会话编号（标签栏 #N 显示） */
  seq: number
  /** 会话已被服务端关闭：标签保留（冻结画面），仅用户 ✕ 才会删除 */
  closed?: boolean
}

interface TabsState {
  tabs: Tab[]
  activeTabId: string | null
  pendingOpen: string[]
  seq: number
  addTab: (connectionId: string, sessionId: string, title: string) => void
  /** 会话被关闭（服务端/主进程事件）：标记关闭并冻结，**不删除标签**（只有用户 ✕ 才删） */
  markClosed: (sessionId: string) => void
  /** 会话被关闭（服务端/主进程事件）：本地清理 + 分组联动（不发 IPC） */
  removeTab: (sessionId: string) => void
  /** 原窗口重开：标签换绑到新会话（编号/标题/位置不变，解除冻结） */
  rebindTab: (oldSessionId: string, newSessionId: string) => void
  /** 用户点标签 ✕：发 closeSession IPC + 本地清理 + 联动 */
  closeTab: (sessionId: string) => void
  removeTabsOfConnection: (connectionId: string) => void
  setActiveTab: (id: string | null) => void
  addPendingOpen: (connectionId: string) => void
  removePendingOpen: (connectionId: string) => void
  /** 点组内标签：激活该会话所在组（编辑组语义） */
  handleTabClick: (sessionId: string) => void
}

export const useTabsStore = create<TabsState>()((set, get) => ({
  tabs: [],
  activeTabId: null,
  pendingOpen: [],
  seq: 0,

  addTab: (connectionId, sessionId, title) =>
    set((s) => ({
      tabs: [...s.tabs, { sessionId, connectionId, title, seq: s.seq + 1, closed: false }],
      seq: s.seq + 1
    })),

  markClosed: (sessionId) => {
    set((s) => ({
      tabs: s.tabs.map((t) => (t.sessionId === sessionId ? { ...t, closed: true } : t))
    }))
    // 已关闭会话退出广播子集，避免空写
    useBroadcastStore.getState().prune(sessionId)
    // 不触碰分屏组：标签留在原地，画面冻结，用户能看到离开前的位置
  },

  removeTab: (sessionId) => {
    set((s) => ({ tabs: s.tabs.filter((t) => t.sessionId !== sessionId) }))
    useBroadcastStore.getState().prune(sessionId)
    useSplitStore.getState().removeSession(sessionId)
  },

  rebindTab: (oldSessionId, newSessionId) => {
    set((s) => ({
      tabs: s.tabs.map((t) =>
        t.sessionId === oldSessionId ? { ...t, sessionId: newSessionId, closed: false } : t
      ),
      activeTabId: s.activeTabId === oldSessionId ? newSessionId : s.activeTabId
    }))
  },

  closeTab: (sessionId) => {
    // globalThis 安全访问：该模块也会被 selftest（node 环境）导入
    const w = (globalThis as { window?: { bastion?: BastionApi } }).window
    w?.bastion?.closeSession(sessionId)
    get().removeTab(sessionId)
  },

  removeTabsOfConnection: (connectionId) =>
    set((s) => ({ tabs: s.tabs.filter((t) => t.connectionId !== connectionId) })),

  setActiveTab: (id) => set({ activeTabId: id }),

  addPendingOpen: (connectionId) => set((s) => ({ pendingOpen: [...s.pendingOpen, connectionId] })),

  removePendingOpen: (connectionId) =>
    set((s) => ({ pendingOpen: s.pendingOpen.filter((x) => x !== connectionId) })),

  handleTabClick: (sessionId) => {
    const { groups } = useSplitStore.getState()
    const gid = findGroupOfSession(groups, sessionId)
    if (gid) {
      useSplitStore.getState().activateSession(gid, sessionId)
    } else {
      // 兜底：会话不在任何组时加入聚焦组
      useSplitStore.getState().addSession(sessionId)
    }
  }
}))
