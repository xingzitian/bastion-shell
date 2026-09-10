/** 编辑组（VS Code Editor Group）模型：分屏树的叶子是"组"，组内是标签栈 */

export type Side = 'left' | 'right' | 'top' | 'bottom'

export interface TerminalGroup {
  id: string
  /** 组内标签栈（顺序即标签顺序） */
  sessionIds: string[]
  /** 活跃标签下标（空组为 -1） */
  activeIndex: number
}

export type GroupTreeNode =
  | { kind: 'group'; id: string }
  | { kind: 'tree'; id: string; orientation: 'h' | 'v'; ratio: number; a: GroupTreeNode; b: GroupTreeNode }

export const MIN_RATIO = 0.15
export const MAX_RATIO = 0.85

let seq = 0
export const newGroupId = (): string => `grp${++seq}${Date.now().toString(36)}`
export const newTreeId = (): string => `tre${++seq}${Date.now().toString(36)}`

export const emptyGroup = (id = newGroupId()): TerminalGroup => ({ id, sessionIds: [], activeIndex: -1 })
export const rootTree = (id = newGroupId()): GroupTreeNode => ({ kind: 'group', id })

/** 把 groupIds 排成 rows×cols 网格树（先按行纵向切，再按列横向切；不足补齐空组） */
export function gridTree(groupIds: string[], rows: number, cols: number): GroupTreeNode {
  const n = Math.max(1, rows) * Math.max(1, cols)
  const ids = groupIds.slice(0, n)
  while (ids.length < n) ids.push(newGroupId())
  return buildGrid(ids, Math.max(1, rows), Math.max(1, cols))
}

function buildGrid(ids: string[], rows: number, cols: number): GroupTreeNode {
  if (rows <= 1 && cols <= 1) return { kind: 'group', id: ids[0] }
  if (rows > 1) {
    const mid = Math.floor(rows / 2)
    const topCount = mid * cols
    return {
      kind: 'tree',
      id: newTreeId(),
      orientation: 'v',
      ratio: 0.5,
      a: buildGrid(ids.slice(0, topCount), mid, cols),
      b: buildGrid(ids.slice(topCount), rows - mid, cols)
    }
  }
  const mid = Math.floor(cols / 2)
  return {
    kind: 'tree',
    id: newTreeId(),
    orientation: 'h',
    ratio: 0.5,
    a: buildGrid(ids.slice(0, mid), 1, mid),
    b: buildGrid(ids.slice(mid), 1, cols - mid)
  }
}

export function collectGroupIds(root: GroupTreeNode): string[] {
  if (root.kind === 'group') return [root.id]
  return [...collectGroupIds(root.a), ...collectGroupIds(root.b)]
}

/** 在组的某侧劈出新组（新组节点 id 由调用方指定并注册） */
export function splitAtGroup(root: GroupTreeNode, groupId: string, side: Side, newGroupNodeId: string): GroupTreeNode {
  if (root.kind === 'group') {
    if (root.id !== groupId) return root
    const orientation: 'h' | 'v' = side === 'left' || side === 'right' ? 'h' : 'v'
    const fresh: GroupTreeNode = { kind: 'group', id: newGroupNodeId }
    return side === 'left' || side === 'top'
      ? { kind: 'tree', id: newTreeId(), orientation, ratio: 0.5, a: fresh, b: { ...root } }
      : { kind: 'tree', id: newTreeId(), orientation, ratio: 0.5, a: { ...root }, b: fresh }
  }
  return { ...root, a: splitAtGroup(root.a, groupId, side, newGroupNodeId), b: splitAtGroup(root.b, groupId, side, newGroupNodeId) }
}

/** 塌缩指定组（兄弟节点顶替）；根为单组时不塌缩 */
export function collapseGroup(root: GroupTreeNode, groupId: string): GroupTreeNode {
  if (root.kind === 'group') return root
  if (containsGroup(root.a, groupId)) {
    if (root.a.kind === 'group' && root.a.id === groupId) return root.b
    return { ...root, a: collapseGroup(root.a, groupId) }
  }
  if (containsGroup(root.b, groupId)) {
    if (root.b.kind === 'group' && root.b.id === groupId) return root.a
    return { ...root, b: collapseGroup(root.b, groupId) }
  }
  return root
}

function containsGroup(root: GroupTreeNode, groupId: string): boolean {
  if (root.kind === 'group') return root.id === groupId
  return containsGroup(root.a, groupId) || containsGroup(root.b, groupId)
}

export const clampRatio = (r: number): number => Math.min(MAX_RATIO, Math.max(MIN_RATIO, r))

export function resizeAt(root: GroupTreeNode, treeId: string, ratio: number): GroupTreeNode {
  if (root.kind === 'group') return root
  if (root.id === treeId) return { ...root, ratio: clampRatio(ratio) }
  return { ...root, a: resizeAt(root.a, treeId, ratio), b: resizeAt(root.b, treeId, ratio) }
}

// ---- 组注册表纯操作 ----

export function addToGroup(
  groups: Record<string, TerminalGroup>,
  groupId: string,
  sessionId: string
): Record<string, TerminalGroup> {
  const g = groups[groupId]
  if (!g) return groups
  const sessionIds = g.sessionIds.includes(sessionId) ? g.sessionIds : [...g.sessionIds, sessionId]
  return { ...groups, [groupId]: { ...g, sessionIds, activeIndex: sessionIds.length - 1 } }
}

export function removeFromGroup(
  groups: Record<string, TerminalGroup>,
  groupId: string,
  sessionId: string
): Record<string, TerminalGroup> {
  const g = groups[groupId]
  if (!g) return groups
  const idx = g.sessionIds.indexOf(sessionId)
  if (idx === -1) return groups
  const sessionIds = g.sessionIds.filter((s) => s !== sessionId)
  let activeIndex = g.activeIndex
  if (idx === g.activeIndex) activeIndex = sessionIds.length > 0 ? Math.min(idx, sessionIds.length - 1) : -1
  else if (idx < g.activeIndex) activeIndex = g.activeIndex - 1
  return { ...groups, [groupId]: { ...g, sessionIds, activeIndex } }
}

export function activateInGroup(
  groups: Record<string, TerminalGroup>,
  groupId: string,
  sessionId: string
): Record<string, TerminalGroup> {
  const g = groups[groupId]
  if (!g) return groups
  const idx = g.sessionIds.indexOf(sessionId)
  if (idx === -1) return groups
  return { ...groups, [groupId]: { ...g, activeIndex: idx } }
}

export function reorderInGroup(
  groups: Record<string, TerminalGroup>,
  groupId: string,
  sessionId: string,
  targetIndex: number
): Record<string, TerminalGroup> {
  const g = groups[groupId]
  if (!g) return groups
  const from = g.sessionIds.indexOf(sessionId)
  if (from === -1) return groups
  const arr = [...g.sessionIds]
  const [item] = arr.splice(from, 1)
  const insertAt = Math.max(0, from < targetIndex ? targetIndex - 1 : targetIndex)
  arr.splice(insertAt, 0, item)
  return { ...groups, [groupId]: { ...g, sessionIds: arr, activeIndex: arr.indexOf(sessionId) } }
}

export function findGroupOfSession(groups: Record<string, TerminalGroup>, sessionId: string): string | null {
  for (const gid of Object.keys(groups)) {
    if (groups[gid].sessionIds.includes(sessionId)) return gid
  }
  return null
}

export function activeSessionOf(groups: Record<string, TerminalGroup>, groupId: string): string | null {
  const g = groups[groupId]
  if (!g) return null
  return g.sessionIds[g.activeIndex] ?? null
}

export function groupSize(groups: Record<string, TerminalGroup>, groupId: string): number {
  return groups[groupId]?.sessionIds.length ?? 0
}

/** 拖放落点判定：四边（劈新组）或 center（移入该组） */
export function nearestSide(
  x: number,
  y: number,
  rect: { left: number; right: number; top: number; bottom: number; width: number; height: number }
): Side | 'center' {
  const dl = x - rect.left
  const dr = rect.right - x
  const dt = y - rect.top
  const db = rect.bottom - y
  const cw = rect.width * 0.25
  const ch = rect.height * 0.25
  if (dl > cw && dr > cw && dt > ch && db > ch) return 'center'
  const min = Math.min(dl, dr, dt, db)
  if (min === dl) return 'left'
  if (min === dr) return 'right'
  if (min === dt) return 'top'
  return 'bottom'
}

/** 吸附判定需要的磁吸距离（px）：新方向必须比旧方向近这么多才切换 */
export const SNAP_PX = 12

/**
 * 磁吸版落点判定（拖拽过程中用，VS Code 手感）：
 * 相对上次判定方向 prev，只有当新方向的距离比 prev 方向近 SNAP_PX 以上时才切换，
 * 否则维持 prev——指针在边角轻微抖动不会来回横跳。
 * 落定（drop）时也应调用本函数（传当前 overlay 方向），保证"看到什么就得到什么"。
 */
export function nearestSideSnap(
  x: number,
  y: number,
  rect: { left: number; right: number; top: number; bottom: number; width: number; height: number },
  prev: Side | 'center' | null
): Side | 'center' {
  const dl = x - rect.left
  const dr = rect.right - x
  const dt = y - rect.top
  const db = rect.bottom - y
  const cw = rect.width * 0.25
  const ch = rect.height * 0.25
  if (dl > cw && dr > cw && dt > ch && db > ch) return 'center'
  const dist: Record<Side, number> = { left: dl, right: dr, top: dt, bottom: db }
  let side: Side = 'left'
  let min = Infinity
  for (const s of ['left', 'right', 'top', 'bottom'] as Side[]) {
    if (dist[s] < min) {
      min = dist[s]
      side = s
    }
  }
  if (prev && prev !== 'center' && prev !== side && dist[prev] - dist[side] < SNAP_PX) {
    return prev // 新方向没比旧方向近 12px 以上 → 磁吸维持
  }
  return side
}
