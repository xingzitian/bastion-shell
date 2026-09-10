import { useState } from 'react'
import type { HighlightRule, TerminalTheme } from '../shared/types'
import { TerminalView } from './TerminalView'
import { nearestSideSnap } from '../lib/groupTree'
import type { Side, TerminalGroup } from '../lib/groupTree'

export const DND_SESSION = 'application/x-bastion-session'

interface Props {
  group: TerminalGroup
  titles: Map<string, string>
  seqs: Map<string, number>
  /** 已关闭（冻结）的会话标签弱化显示 */
  closedIds: Set<string>
  activeSessionId: string | null
  focused: boolean
  broadcastIds: string[]
  broadcastTargets: string[]
  highlightRules: HighlightRule[]
  theme: TerminalTheme
  onFocus: () => void
  onActivate: (sessionId: string) => void
  onCloseTab: (sessionId: string) => void
  onToggleBroadcast: (sessionId: string) => void
  /** 组内标签拖拽重排 */
  onReorder: (sessionId: string, index: number) => void
  /** 会话拖到组任意位置（含标签栏空白）：center=移入本组；四边=以本组为锚劈出新组 */
  onDrop: (side: Side | 'center', sessionId: string) => void
  /** 右键标签：向指定方向分屏（该标签带着会话去新组） */
  onSplit: (side: Side, sessionId: string) => void
  /** 冻结标签上点 🔌：在原窗口重开新会话（保留历史记录） */
  onReopen: (sessionId: string) => void
}

interface TabMenuState {
  x: number
  y: number
  sessionId: string
}

/**
 * 编辑组（VS Code Editor Group）：
 * 顶部是组内标签栏（#编号/📡/✕），下方是该组活跃会话的终端；
 * 整组都是拖放目标（标签栏空白处也算），右键标签可向四向分屏。
 */
export function GroupView(props: Props) {
  const { group, titles, seqs, closedIds, activeSessionId, focused, broadcastIds, broadcastTargets, highlightRules, theme } = props
  const [reorderIdx, setReorderIdx] = useState<number | null>(null)
  const [dropSide, setDropSide] = useState<Side | 'center' | null>(null)
  const [menu, setMenu] = useState<TabMenuState | null>(null)
  const activeSid = group.sessionIds[group.activeIndex] ?? null

  const handleGroupDragOver = (e: React.DragEvent): void => {
    if (e.dataTransfer.types.includes(DND_SESSION)) {
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
      // 磁吸：以当前 overlay 方向为基准，新方向必须近 12px 以上才切换
      setDropSide((prev) => nearestSideSnap(e.clientX, e.clientY, rect, prev))
    }
  }

  const handleGroupDrop = (e: React.DragEvent): void => {
    e.preventDefault()
    const sid = e.dataTransfer.getData(DND_SESSION)
    setDropSide(null)
    if (!sid) return
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    // 落定也用磁吸判定（传当前 overlay 方向）——看到什么方向就得到什么方向
    props.onDrop(nearestSideSnap(e.clientX, e.clientY, rect, dropSide), sid)
  }

  return (
    <div
      className={`group-view ${focused ? 'focused' : ''}`}
      style={{ flex: 1 }}
      onClick={() => props.onFocus()}
      onDragOver={handleGroupDragOver}
      onDragLeave={() => setDropSide(null)}
      onDrop={handleGroupDrop}
    >
      <div className="group-tabs">
        {group.sessionIds.map((sid, i) => (
          <div
            key={sid}
            className={`group-tab ${sid === activeSid ? 'active' : ''} ${closedIds.has(sid) ? 'closed' : ''} ${reorderIdx === i ? 'drop-before' : ''}`}
            draggable
            title={
              closedIds.has(sid)
                ? '会话已关闭，画面冻结（可回看历史）；🔌 原窗口重开 · ✕ 删除标签'
                : '点击切换；拖动可重排/移到其他组或边缘劈新组；右键可向指定方向分屏'
            }
            onDragStart={(e) => {
              e.dataTransfer.setData(DND_SESSION, sid)
              e.dataTransfer.effectAllowed = 'move'
            }}
            onDragOver={(e) => {
              if (e.dataTransfer.types.includes(DND_SESSION)) {
                e.preventDefault()
                e.stopPropagation()
                const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
                const before = e.clientX < rect.left + rect.width / 2
                setReorderIdx(before ? i : i + 1)
              }
            }}
            onDrop={(e) => {
              e.preventDefault()
              e.stopPropagation()
              const sid2 = e.dataTransfer.getData(DND_SESSION)
              setReorderIdx(null)
              if (!sid2) return
              if (group.sessionIds.includes(sid2)) {
                props.onReorder(sid2, reorderIdx ?? group.sessionIds.length)
              } else {
                // 跨组拖放同样走磁吸判定（与组级 drop 一致）
                const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
                props.onDrop(nearestSideSnap(e.clientX, e.clientY, rect, dropSide), sid2)
              }
            }}
            onDragEnd={() => setReorderIdx(null)}
            onClick={(e) => {
              e.stopPropagation()
              props.onActivate(sid)
            }}
            onContextMenu={(e) => {
              e.preventDefault()
              e.stopPropagation()
              setMenu({ x: e.clientX, y: e.clientY, sessionId: sid })
            }}
          >
            <span
              className={`group-tab-broadcast ${broadcastIds.includes(sid) ? 'on' : ''}`}
              title={broadcastIds.includes(sid) ? '已加入广播子集，点击退出' : '加入广播子集'}
              onClick={(e) => {
                e.stopPropagation()
                props.onToggleBroadcast(sid)
              }}
            >
              📡
            </span>
            <span className="group-tab-title">
              <span className="tab-seq">#{seqs.get(sid)}</span> {titles.get(sid)}
              {closedIds.has(sid) && <span className="tab-closed-badge">⛔已断开</span>}
            </span>
            {closedIds.has(sid) && (
              <span
                className="group-tab-reopen"
                title="在本窗口重开新会话（历史记录保留，无需重新分屏）"
                onClick={(e) => {
                  e.stopPropagation()
                  props.onReopen(sid)
                }}
              >
                🔌
              </span>
            )}
            <span
              className="group-tab-close"
              onClick={(e) => {
                e.stopPropagation()
                props.onCloseTab(sid)
              }}
            >
              ✕
            </span>
          </div>
        ))}
        {group.sessionIds.length === 0 && (
          <div className="group-tabs-empty">空组 —— 拖会话进来，或使用左侧「新会话/批量」</div>
        )}
      </div>

      <div className="group-body">
        {activeSid ? (
          <>
            <TerminalView
              sessionId={activeSid}
              active={activeSid === activeSessionId}
              broadcastTargets={broadcastTargets}
              highlightRules={highlightRules}
              theme={theme}
            />
            {group.sessionIds
              .filter((s) => s !== activeSid)
              .map((sid) => (
                <div key={sid} style={{ display: 'none' }}>
                  <TerminalView
                    sessionId={sid}
                    active={false}
                    broadcastTargets={broadcastTargets}
                    highlightRules={highlightRules}
                    theme={theme}
                  />
                </div>
              ))}
          </>
        ) : (
          <div className="group-empty">
            空组
            <br />
            <span style={{ fontSize: 11, opacity: 0.7 }}>拖会话到中央/标签栏空白 = 移入本组；拖到边缘 = 劈出新组</span>
          </div>
        )}
      </div>

      {dropSide && (
        <div className="split-drop-overlay">
          <div className={`quad ${dropSide === 'left' ? 'show left' : 'left'}`} />
          <div className={`quad ${dropSide === 'right' ? 'show right' : 'right'}`} />
          <div className={`quad ${dropSide === 'top' ? 'show top' : 'top'}`} />
          <div className={`quad ${dropSide === 'bottom' ? 'show bottom' : 'bottom'}`} />
          {dropSide === 'center' && <div className="center show" />}
        </div>
      )}

      {menu && (
        <>
          <div className="menu-backdrop" onClick={() => setMenu(null)} onContextMenu={(e) => e.preventDefault()} />
          <div className="tab-menu" style={{ left: menu.x, top: menu.y }}>
            <div className="menu-item" onClick={() => { setMenu(null); props.onSplit('right', menu.sessionId) }}>
              向右分屏
            </div>
            <div className="menu-item" onClick={() => { setMenu(null); props.onSplit('bottom', menu.sessionId) }}>
              向下分屏
            </div>
            <div className="menu-item" onClick={() => { setMenu(null); props.onSplit('left', menu.sessionId) }}>
              向左分屏
            </div>
            <div className="menu-item" onClick={() => { setMenu(null); props.onSplit('top', menu.sessionId) }}>
              向上分屏
            </div>
            <div className="menu-sep" />
            <div className="menu-item danger" onClick={() => { setMenu(null); props.onCloseTab(menu.sessionId) }}>
              关闭标签
            </div>
          </div>
        </>
      )}
    </div>
  )
}
