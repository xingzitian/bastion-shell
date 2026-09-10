import { create } from 'zustand'
import * as GT from '../lib/groupTree'
import type { GroupTreeNode, Side, TerminalGroup } from '../lib/groupTree'
import { useTabsStore } from './tabs'

/**
 * 编辑组模型：
 * - 树叶子 = 组（组内标签栈）；每个会话恰好属于一个组，无"隐藏池"
 * - 单页模式 = 单组；分屏 = 拆出多组
 * - 新会话/批量一律追加到聚焦组；组空自动塌缩；退出分屏合并回单组
 */
interface SplitState {
  tree: GroupTreeNode
  groups: Record<string, TerminalGroup>
  focusedGroupId: string | null
  /** 一键重排为 rows×cols 网格（保留全部会话，轮询分配到各组） */
  splitToGrid: (rows: number, cols: number) => void
  /** 合并所有组回单组（保留全部会话） */
  mergeAll: () => void
  focusGroup: (groupId: string) => void
  activateSession: (groupId: string, sessionId: string) => void
  addSession: (sessionId: string) => void
  moveSessionTo: (sessionId: string, targetGroupId: string) => void
  splitWithSession: (anchorGroupId: string, side: Side, sessionId: string) => void
  reorderTabs: (groupId: string, sessionId: string, index: number) => void
  removeSession: (sessionId: string) => void
  /** 原窗口重开：分组内的会话 id 换绑（组与位置不变） */
  replaceSessionId: (oldSessionId: string, newSessionId: string) => void
  resize: (treeId: string, ratio: number) => void
  activeSessionId: () => string | null
}

const ROOT_ID = GT.newGroupId()
const initialTree = GT.rootTree(ROOT_ID)
const initialGroups: Record<string, TerminalGroup> = { [ROOT_ID]: GT.emptyGroup(ROOT_ID) }

export const useSplitStore = create<SplitState>()((set, get) => ({
  tree: initialTree,
  groups: initialGroups,
  focusedGroupId: ROOT_ID,

  activeSessionId: () => {
    const { groups, focusedGroupId, tree } = get()
    const gid = focusedGroupId ?? (tree.kind === 'group' ? tree.id : GT.collectGroupIds(tree)[0])
    return GT.activeSessionOf(groups, gid)
  },

  splitToGrid: (rows, cols) => {
    const { tree, groups } = get()
    const n = Math.max(1, rows) * Math.max(1, cols)
    // 收集现有会话（按组顺序、去重），再轮询分配到 n 个新组
    const sessions: string[] = []
    for (const gid of GT.collectGroupIds(tree)) {
      const g = groups[gid]
      if (!g) continue
      for (const s of g.sessionIds) if (!sessions.includes(s)) sessions.push(s)
    }
    const newGroups: TerminalGroup[] = Array.from({ length: n }, () => ({
      id: GT.newGroupId(),
      sessionIds: [],
      activeIndex: -1
    }))
    sessions.forEach((sid, i) => newGroups[i % n].sessionIds.push(sid))
    const activeTab = useTabsStore.getState().activeTabId
    for (const g of newGroups) {
      const idx = activeTab ? g.sessionIds.indexOf(activeTab) : -1
      g.activeIndex = g.sessionIds.length === 0 ? -1 : idx >= 0 ? idx : 0
    }
    const groupsMap: Record<string, TerminalGroup> = {}
    for (const g of newGroups) groupsMap[g.id] = g
    const nextTree = GT.gridTree(newGroups.map((g) => g.id), rows, cols)
    const focusGid = newGroups.find((g) => activeTab && g.sessionIds.includes(activeTab))?.id ?? newGroups[0].id
    set({ tree: nextTree, groups: groupsMap, focusedGroupId: focusGid })
  },

  mergeAll: () => {
    const { tree, groups } = get()
    const sessions: string[] = []
    for (const gid of GT.collectGroupIds(tree)) {
      const g = groups[gid]
      if (!g) continue
      for (const s of g.sessionIds) if (!sessions.includes(s)) sessions.push(s)
    }
    const activeTab = useTabsStore.getState().activeTabId
    const rootId = GT.collectGroupIds(tree)[0] ?? GT.newGroupId()
    const idx = activeTab ? sessions.indexOf(activeTab) : -1
    const rootGroup: TerminalGroup = {
      id: rootId,
      sessionIds: sessions,
      activeIndex: sessions.length === 0 ? -1 : idx >= 0 ? idx : 0
    }
    set({ tree: GT.rootTree(rootId), groups: { [rootId]: rootGroup }, focusedGroupId: rootId })
  },

  focusGroup: (groupId) => {
    set({ focusedGroupId: groupId })
    const a = GT.activeSessionOf(get().groups, groupId)
    useTabsStore.getState().setActiveTab(a)
  },

  activateSession: (groupId, sessionId) => {
    set((s) => ({ groups: GT.activateInGroup(s.groups, groupId, sessionId), focusedGroupId: groupId }))
    useTabsStore.getState().setActiveTab(sessionId)
  },

  addSession: (sessionId) => {
    const { groups, focusedGroupId, tree } = get()
    const gid =
      focusedGroupId && groups[focusedGroupId]
        ? focusedGroupId
        : tree.kind === 'group'
          ? tree.id
          : GT.collectGroupIds(tree)[0]
    set({ groups: GT.addToGroup(groups, gid, sessionId) })
    useTabsStore.getState().setActiveTab(sessionId)
  },

  moveSessionTo: (sessionId, targetGroupId) => {
    const { groups } = get()
    const fromId = GT.findGroupOfSession(groups, sessionId)
    if (!fromId || fromId === targetGroupId) return
    let ng = GT.removeFromGroup(groups, fromId, sessionId)
    ng = GT.addToGroup(ng, targetGroupId, sessionId)
    let nt = get().tree
    if (GT.groupSize(ng, fromId) === 0 && nt.kind !== 'group') {
      nt = GT.collapseGroup(nt, fromId)
      const rest: Record<string, TerminalGroup> = { ...ng }
      delete rest[fromId]
      ng = rest
    }
    set({ groups: ng, tree: nt, focusedGroupId: targetGroupId })
    useTabsStore.getState().setActiveTab(sessionId)
  },

  splitWithSession: (anchorGroupId, side, sessionId) => {
    const { groups, tree } = get()
    const fromId = GT.findGroupOfSession(groups, sessionId)
    let ng = fromId ? GT.removeFromGroup(groups, fromId, sessionId) : groups
    const newId = GT.newGroupId()
    ng = { ...ng, [newId]: { id: newId, sessionIds: [sessionId], activeIndex: 0 } }
    let nt = GT.splitAtGroup(tree, anchorGroupId, side, newId)
    if (fromId && GT.groupSize(ng, fromId) === 0 && nt.kind !== 'group') {
      nt = GT.collapseGroup(nt, fromId)
      const rest: Record<string, TerminalGroup> = { ...ng }
      delete rest[fromId]
      ng = rest
    }
    set({ groups: ng, tree: nt, focusedGroupId: newId })
    useTabsStore.getState().setActiveTab(sessionId)
  },

  reorderTabs: (groupId, sessionId, index) => {
    set((s) => ({ groups: GT.reorderInGroup(s.groups, groupId, sessionId, index) }))
  },

  removeSession: (sessionId) => {
    const { groups, tree, focusedGroupId } = get()
    const gid = GT.findGroupOfSession(groups, sessionId)
    if (!gid) return
    let ng = GT.removeFromGroup(groups, gid, sessionId)
    let nt = tree
    let nextFocused = focusedGroupId
    if (GT.groupSize(ng, gid) === 0 && nt.kind !== 'group') {
      nt = GT.collapseGroup(nt, gid)
      const rest: Record<string, TerminalGroup> = { ...ng }
      delete rest[gid]
      ng = rest
      if (focusedGroupId === gid) {
        nextFocused = GT.collectGroupIds(nt)[0] ?? null
      }
    }
    set({ groups: ng, tree: nt, focusedGroupId: nextFocused })
    const nextActive = GT.activeSessionOf(ng, nextFocused ?? GT.collectGroupIds(nt)[0])
    useTabsStore.getState().setActiveTab(nextActive)
  },

  replaceSessionId: (oldSessionId, newSessionId) =>
    set((s) => {
      let changed = false
      const groups: Record<string, TerminalGroup> = {}
      for (const gid of Object.keys(s.groups)) {
        const g = s.groups[gid]
        if (!g.sessionIds.includes(oldSessionId)) {
          groups[gid] = g
          continue
        }
        changed = true
        groups[gid] = { ...g, sessionIds: g.sessionIds.map((x) => (x === oldSessionId ? newSessionId : x)) }
      }
      return changed ? { groups } : s
    }),

  resize: (treeId, ratio) => set((s) => ({ tree: GT.resizeAt(s.tree, treeId, ratio) }))
}))
