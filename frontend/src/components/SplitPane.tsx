import type { HighlightRule, TerminalTheme } from '../shared/types'
import { GroupView } from './GroupView'
import type { GroupTreeNode, Side, TerminalGroup } from '../lib/groupTree'

interface Props {
  node: GroupTreeNode
  groups: Record<string, TerminalGroup>
  titles: Map<string, string>
  seqs: Map<string, number>
  /** 已关闭（冻结）的会话，标签以弱化样式显示 */
  closedIds: Set<string>
  activeSessionId: string | null
  focusedGroupId: string | null
  broadcastIds: string[]
  broadcastTargets: string[]
  highlightRules: HighlightRule[]
  theme: TerminalTheme
  onFocusGroup: (groupId: string) => void
  onActivate: (groupId: string, sessionId: string) => void
  onCloseTab: (sessionId: string) => void
  onToggleBroadcast: (sessionId: string) => void
  onReorder: (groupId: string, sessionId: string, index: number) => void
  onMoveSession: (sessionId: string, targetGroupId: string) => void
  onSplitSession: (anchorGroupId: string, side: Side, sessionId: string) => void
  /** 右键标签向指定方向分屏：锚 = 该标签当前所在组 */
  onSplitTab: (side: Side, sessionId: string) => void
  /** 冻结标签 🔌 原窗口重开 */
  onReopen: (sessionId: string) => void
  onResize: (treeId: string, ratio: number) => void
}

/** 组（叶子）最小可见尺寸：自由拖分隔条也不能把组压成看不见的缝 */
const GROUP_MIN_W = 150
const GROUP_MIN_H = 90

/** 编辑组树渲染器：递归二分，叶子渲染 GroupView，分隔条拖动调比例 */
export function SplitPane(props: Props) {
  const { node, groups } = props

  if (node.kind === 'group') {
    const group = groups[node.id] ?? { id: node.id, sessionIds: [], activeIndex: -1 }
    return (
      <GroupView
        group={group}
        titles={props.titles}
        seqs={props.seqs}
        closedIds={props.closedIds}
        activeSessionId={props.activeSessionId}
        focused={node.id === props.focusedGroupId}
        broadcastIds={props.broadcastIds}
        broadcastTargets={props.broadcastTargets}
        highlightRules={props.highlightRules}
        theme={props.theme}
        onFocus={() => props.onFocusGroup(node.id)}
        onActivate={(sid) => props.onActivate(node.id, sid)}
        onCloseTab={props.onCloseTab}
        onToggleBroadcast={props.onToggleBroadcast}
        onReorder={(sid, idx) => props.onReorder(node.id, sid, idx)}
        onDrop={(side, sid) => (side === 'center' ? props.onMoveSession(sid, node.id) : props.onSplitSession(node.id, side, sid))}
        onSplit={(side, sid) => props.onSplitTab(side, sid)}
        onReopen={props.onReopen}
      />
    )
  }

  const orientation = node.orientation
  // 组（叶子）设最小宽高：拖分隔条/缩放窗口时组不会被压成看不见的缝
  const minA: React.CSSProperties = node.a.kind === 'group' ? { minWidth: GROUP_MIN_W, minHeight: GROUP_MIN_H } : { minWidth: 0, minHeight: 0 }
  const minB: React.CSSProperties = node.b.kind === 'group' ? { minWidth: GROUP_MIN_W, minHeight: GROUP_MIN_H } : { minWidth: 0, minHeight: 0 }
  return (
    <div className={`split-tree ${orientation}`} style={{ flex: 1 }}>
      <div style={{ flexBasis: `${node.ratio * 100}%`, flexGrow: 0, flexShrink: 0, display: 'flex', ...minA }}>
        <SplitPane {...props} node={node.a} />
      </div>
      <div
        className="split-divider"
        onMouseDown={(e) => startResize(e, node.id, orientation, props.onResize)}
      />
      <div style={{ flex: 1, display: 'flex', ...minB }}>
        <SplitPane {...props} node={node.b} />
      </div>
    </div>
  )
}

function startResize(
  e: React.MouseEvent,
  treeId: string,
  orientation: 'h' | 'v',
  onResize: (id: string, ratio: number) => void
): void {
  e.preventDefault()
  const container = (e.currentTarget.parentElement as HTMLElement) ?? null
  if (!container) return
  const move = (ev: MouseEvent): void => {
    const rect = container.getBoundingClientRect()
    const ratio =
      orientation === 'h' ? (ev.clientX - rect.left) / rect.width : (ev.clientY - rect.top) / rect.height
    onResize(treeId, ratio)
  }
  const up = (): void => {
    window.removeEventListener('mousemove', move)
    window.removeEventListener('mouseup', up)
  }
  window.addEventListener('mousemove', move)
  window.addEventListener('mouseup', up)
}
