import { bus } from '../lib/bus'
import { getTerminalManager } from '../lib/terminalManager'
import { useConnectionsStore, userClosedIds } from './connections'
import { useTabsStore } from './tabs'
import { useBroadcastStore } from './broadcast'
import { useTransfersStore } from './transfers'
import { usePrefsStore } from './prefs'
import { useForwardStore } from './forwards'

/**
 * IPC 事件 → store 的集中订阅（模块初始化一次）。
 * 所有领域事件都在这里落地到 store，UI 组件只读 store——
 * 从机制上消除"effect 闭包过期"这类 bug。
 */
let initialized = false

export function initSubscriptions(): void {
  if (initialized) return
  initialized = true

  void window.bastion.listProfiles().then((p) => usePrefsStore.getState().setProfiles(p))
  void window.bastion.listQuickCommands().then((q) => usePrefsStore.getState().setQuickCommands(q))
  void window.bastion.listHighlightRules().then((r) => usePrefsStore.getState().setHighlightRules(r))
  void window.bastion.getTheme().then((t) => {
    if (t) usePrefsStore.getState().setTheme(t)
  })

  window.bastion.onConnectionStatus((p) => {
    if (p.status === 'closed' && userClosedIds.has(p.connectionId)) {
      // 用户主动断开：移除卡片与所属标签，终端实例与广播子集一并清理
      userClosedIds.delete(p.connectionId)
      useConnectionsStore.getState().remove(p.connectionId)
      const sids = useTabsStore
        .getState()
        .tabs.filter((t) => t.connectionId === p.connectionId)
        .map((t) => t.sessionId)
      for (const sid of sids) {
        getTerminalManager().disposeSession(sid)
        useBroadcastStore.getState().prune(sid)
      }
      useTabsStore.getState().removeTabsOfConnection(p.connectionId)
      return
    }
    useConnectionsStore.getState().upsert(p.connectionId, { status: p.status, attempt: p.attempt })
    if (p.status === 'closed') {
      useTabsStore.getState().removePendingOpen(p.connectionId)
      bus.emit('conn:closed', { connectionId: p.connectionId })
      bus.emit('ui:toast', { kind: 'error', text: `连接已断开：${p.reason ?? '未知原因'}` })
    }
  })

  window.bastion.onRestored((p) => {
    bus.emit('ui:toast', { kind: 'success', text: `连接已恢复，${p.sessionCount} 个会话已重建（无需重新选择机器）` })
  })

  window.bastion.onHostKey((p) => {
    bus.emit('ui:toast', { kind: 'info', text: `主机指纹 ${p.fingerprint}${p.isNew ? '（首次连接，已信任）' : ''}` })
  })

  window.bastion.onSessionClosed((p) => {
    // 会话结束 ≠ 标签删除：冻结画面保留现场，用户可回看离开前的位置
    useTabsStore.getState().markClosed(p.sessionId)
  })

  window.bastion.onZmodemEvent((p) => {
    if (p.type === 'start') {
      useTransfersStore.getState().start(p)
    } else if (p.type === 'progress') {
      useTransfersStore.getState().progress(p.transferId, p.bytesSent)
    } else if (p.type === 'end') {
      useTransfersStore.getState().remove(p.transferId)
      bus.emit('ui:toast', {
        kind: 'success',
        text: `${p.direction === 'receive' ? '接收' : '上传'}完成：${p.name ?? ''}${p.message ? `（${p.message}）` : ''}`
      })
    } else if (p.type === 'error') {
      useTransfersStore.getState().remove(p.transferId)
      bus.emit('ui:toast', { kind: 'error', text: p.message ?? 'ZMODEM 传输失败' })
    } else if (p.type === 'info') {
      bus.emit('ui:toast', { kind: 'info', text: p.message ?? '' })
    }
  })

  window.bastion.onForwardStatus((p) => {
    useForwardStore.getState().setStatus(p.ruleId, p.status, p.message)
  })
}
