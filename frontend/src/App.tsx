import { useCallback, useEffect, useRef, useState } from 'react'
import type { AuthPromptPayload, ConnectionProfile, DeployTask } from './shared/types'
import { ConnectDialog, ConnectDialogResult } from './components/ConnectDialog'
import { AuthPromptDialog } from './components/AuthPromptDialog'
import { QuickCommandsDialog } from './components/QuickCommandsDialog'
import { HighlightRulesDialog } from './components/HighlightRulesDialog'
import { ThemeDialog } from './components/ThemeDialog'
import { BatchConnectDialog } from './components/BatchConnectDialog'
import { SplitPane } from './components/SplitPane'
import { BroadcastBar } from './components/BroadcastBar'
import { SyncDialog } from './components/SyncDialog'
import { PortsPanel } from './components/PortsPanel'
import { DeployDialog } from './components/DeployDialog'
import { bus } from './lib/bus'
import { findGroupOfSession, collectGroupIds } from './lib/groupTree'
import { getTerminalManager } from './lib/terminalManager'
import { useTabsStore } from './store/tabs'
import { useSplitStore } from './store/split'
import { useConnectionsStore, userClosedIds, ConnectionInfo } from './store/connections'
import { useBroadcastStore } from './store/broadcast'
import { usePrefsStore } from './store/prefs'
import { useTransfersStore } from './store/transfers'
import { useTerminalInfoStore } from './store/terminalInfo'
import { useUploadStore, OVERWRITE_LABEL, OVERWRITE_HINT } from './store/upload'
import { initSubscriptions } from './store/subscriptions'

interface Toast {
  id: number
  kind: 'info' | 'error' | 'success'
  text: string
}

let toastSeq = 1

const statusText = (s: ConnectionInfo['status']): string =>
  ({
    connecting: '连接中…',
    connected: '已认证 · 可复用',
    reconnecting: '掉线 · 自动重连中',
    closed: '已断开'
  })[s]

// 领域事件订阅一次（模块级，StrictMode 安全）
initSubscriptions()

export default function App() {
  // ---- store 快照 ----
  const profiles = usePrefsStore((s) => s.profiles)
  const quickCommands = usePrefsStore((s) => s.quickCommands)
  const highlightRules = usePrefsStore((s) => s.highlightRules)
  const theme = usePrefsStore((s) => s.theme)
  const connections = useConnectionsStore((s) => s.connections)
  const tabs = useTabsStore((s) => s.tabs)
  const activeTabId = useTabsStore((s) => s.activeTabId)
  const pendingOpen = useTabsStore((s) => s.pendingOpen)
  const broadcastIds = useBroadcastStore((s) => s.ids)
  const broadcastMaster = useBroadcastStore((s) => s.master)
  const tree = useSplitStore((s) => s.tree)
  const groups = useSplitStore((s) => s.groups)
  const focusedGroupId = useSplitStore((s) => s.focusedGroupId)
  const transfers = useTransfersStore((s) => s.transfers)
  const termSizes = useTerminalInfoStore((s) => s.sizes)
  const uploadOverwrite = useUploadStore((s) => s.overwrite)

  // ---- App 本地状态（弹窗/瞬态）----
  const [showConnect, setShowConnect] = useState<{ open: boolean; preset?: ConnectionProfile }>({ open: false })
  const [authPrompt, setAuthPrompt] = useState<AuthPromptPayload | null>(null)
  const [promptQueue, setPromptQueue] = useState<AuthPromptPayload[]>([])
  const [toasts, setToasts] = useState<Toast[]>([])
  const [diagnoseMsg, setDiagnoseMsg] = useState<string | null>(null)
  const [showQcManager, setShowQcManager] = useState(false)
  const [showHlManager, setShowHlManager] = useState(false)
  const [showTheme, setShowTheme] = useState(false)
  const [showSync, setShowSync] = useState(false)
  const [showPorts, setShowPorts] = useState<{ connectionId: string; profileId: string } | null>(null)
  const [deployTarget, setDeployTarget] = useState<ConnectionInfo | null>(null)
  const [deployProgress, setDeployProgress] = useState<{
    taskName: string
    hosts: { host: string; status: 'pending' | 'running' | 'done' | 'error' }[]
  } | null>(null)
  const [batchTarget, setBatchTarget] = useState<ConnectionInfo | null>(null)
  const [uiTheme, setUiTheme] = useState<'light' | 'dark'>(() => {
    try {
      return localStorage.getItem('bastion-shell-ui-theme') === 'dark' ? 'dark' : 'light'
    } catch {
      return 'light'
    }
  })

  useEffect(() => {
    document.body.setAttribute('data-ui-theme', uiTheme)
    try {
      localStorage.setItem('bastion-shell-ui-theme', uiTheme)
    } catch {
      /* ignore */
    }
  }, [uiTheme])

  const toast = useCallback((kind: Toast['kind'], text: string) => {
    const id = toastSeq++
    setToasts((t) => [...t, { id, kind, text }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 6000)
  }, [])

  // ---- 总线订阅 ----
  useEffect(() => {
    const offToast = bus.on('ui:toast', (p) => toast(p.kind, p.text))
    const offClosed = bus.on('conn:closed', (p) => {
      setPromptQueue((q) => q.filter((x) => x.connectionId !== p.connectionId))
      setAuthPrompt((a) => (a && a.connectionId === p.connectionId ? null : a))
    })
    return () => {
      offToast()
      offClosed()
    }
  }, [toast])

  // ---- 认证提示队列 ----
  useEffect(() => {
    const offPrompt = window.bastion.onAuthPrompt((p) => {
      setPromptQueue((q) => (q.some((x) => x.nonce === p.nonce) ? q : [...q, p]))
    })
    return offPrompt
  }, [])

  useEffect(() => {
    if (!authPrompt && promptQueue.length > 0) {
      setAuthPrompt(promptQueue[0])
      setPromptQueue((q) => q.slice(1))
    }
  }, [authPrompt, promptQueue])

  // ---- 渲染层错误上报 ----
  useEffect(() => {
    const onErr = (e: ErrorEvent): void => {
      window.bastion.logToMain('error', `uncaught: ${e.message ?? String(e.error)}`)
    }
    const onRej = (e: PromiseRejectionEvent): void => {
      window.bastion.logToMain('error', `unhandledrejection: ${String(e.reason)}`)
    }
    window.addEventListener('error', onErr)
    window.addEventListener('unhandledrejection', onRej)
    return () => {
      window.removeEventListener('error', onErr)
      window.removeEventListener('unhandledrejection', onRej)
    }
  }, [])

  // ---- 布局变化后自动重新适配所有终端 ----
  // 分屏/合并/跨组移动会卸载重建 GroupView，flex 布局跨多帧才稳定；
  // 树一变等 150ms + 双 rAF（再等两帧）强制 fit，兜底 ResizeObserver 没抓到最终尺寸的情况。
  useEffect(() => {
    const t = setTimeout(() => {
      requestAnimationFrame(() => requestAnimationFrame(() => getTerminalManager().fitAll()))
    }, 150)
    return () => clearTimeout(t)
  }, [tree])

  // ---- 新会话：开会话 + 入聚焦组（编辑组语义）----
  const openSessionFor = useCallback(
    async (conn: ConnectionInfo) => {
      try {
        const { sessionId } = await window.bastion.openSession({ connectionId: conn.id, cols: 100, rows: 30 })
        useTabsStore.getState().addTab(conn.id, sessionId, `${conn.username}@${conn.host}`)
        useSplitStore.getState().addSession(sessionId)
        // 诊断：记录开会话后的标签/分组全貌，便于定位"新会话夺舍老窗口"
        const t = useTabsStore.getState()
        const g = useSplitStore.getState()
        window.bastion.logToMain(
          'info',
          `[open-session] sid=${sessionId.slice(0, 8)} tabs=[${t.tabs.map((x) => x.sessionId.slice(0, 8)).join(',')}] groups=${Object.values(g.groups)
            .map((gr) => `[${gr.sessionIds.map((s) => s.slice(0, 8)).join(',')}]`)
            .join(' ')}`
        )
      } catch (err) {
        toast('error', `打开会话失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  // ---- 自动重连场景：等 connected 状态再开第一个会话 ----
  useEffect(() => {
    if (pendingOpen.length === 0) return
    for (const id of [...pendingOpen]) {
      const c = connections.find((x) => x.id === id)
      if (!c) continue
      if (c.status === 'connected') {
        useTabsStore.getState().removePendingOpen(id)
        void openSessionFor(c)
      } else if (c.status === 'closed') {
        useTabsStore.getState().removePendingOpen(id)
      }
    }
  }, [pendingOpen, connections, openSessionFor])

  // ---- 认证应答 ----
  const submitAuth = useCallback(
    (value: string) => {
      if (!authPrompt) return
      const p = authPrompt
      setAuthPrompt(null)
      void window.bastion.answerAuth(p.nonce, value)
    },
    [authPrompt]
  )

  const cancelAuth = useCallback(() => {
    if (!authPrompt) return
    const p = authPrompt
    setAuthPrompt(null)
    void window.bastion.answerAuth(p.nonce, null)
  }, [authPrompt])

  // ---- 原窗口重开（冻结标签 🔌）：终端换绑保留历史，无需重新分屏 ----
  const reopenForRef = useRef<string | null>(null)

  const reopenInPlace = useCallback(
    async (oldSessionId: string, connectionId: string) => {
      try {
        const { sessionId: newSessionId } = await window.bastion.openSession({
          connectionId,
          cols: 100,
          rows: 30
        })
        getTerminalManager().rebindSession(oldSessionId, newSessionId)
        useTabsStore.getState().rebindTab(oldSessionId, newSessionId)
        useSplitStore.getState().replaceSessionId(oldSessionId, newSessionId)
        toast('success', '已在本窗口重开新会话（历史记录保留）')
      } catch (err) {
        toast('error', `原窗口重开失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const requestReopen = useCallback(
    (sessionId: string) => {
      const tab = useTabsStore.getState().tabs.find((t) => t.sessionId === sessionId)
      if (!tab || !tab.closed) return
      const conn = useConnectionsStore.getState().connections.find((c) => c.id === tab.connectionId)
      if (conn && conn.status === 'connected') {
        // 连接还活着：直接在同一连接上开新会话接回本窗口
        void reopenInPlace(sessionId, conn.id)
        return
      }
      if (conn && conn.status === 'reconnecting') {
        toast('info', '该连接正在自动重连中，稍候再试')
        return
      }
      if (!conn?.profile) {
        toast('error', '该会话所属连接已不存在，无法原窗口重开')
        return
      }
      // 连接已断开：带档案打开连接弹窗，认证成功后自动接回本窗口
      reopenForRef.current = sessionId
      setShowConnect({ open: true, preset: conn.profile })
    },
    [toast, reopenInPlace]
  )

  // ---- 连接 ----
  const connect = useCallback(
    async (profile: ConnectionProfile, opts: ConnectDialogResult, reopenSessionId?: string | null) => {
      const pendingId = `pending-${Date.now()}`
      const info: ConnectionInfo = { id: pendingId, host: profile.host, username: profile.username, profile, status: 'connecting' }
      const upsert = useConnectionsStore.getState().upsert
      upsert(pendingId, info)
      try {
        const { connectionId } = await window.bastion.connect({
          profile,
          password: opts.password,
          bastionMode: 'menu',
          autoReconnect: opts.autoReconnect,
          useStoredPassword: opts.useStoredPassword,
          savePassword: opts.savePassword,
          authMethod: opts.authMethod,
          privateKeyPath: opts.privateKeyPath,
          passphrase: opts.passphrase,
          useStoredPassphrase: opts.useStoredPassphrase,
          savePassphrase: opts.savePassphrase
        })
        upsert(connectionId, { host: profile.host, username: profile.username, profile })
        useConnectionsStore.getState().remove(pendingId)
        if (reopenSessionId) {
          // 原窗口重开：连接就绪后直接在本窗口开会话（不经过 pendingOpen 常规流程）
          await reopenInPlace(reopenSessionId, connectionId)
        } else {
          useTabsStore.getState().addPendingOpen(connectionId)
        }
        if (opts.useStoredPassword) {
          toast('info', '已使用本机加密保存的密码（仅需输入 MFA）')
        }
      } catch (err) {
        useConnectionsStore.getState().remove(pendingId)
        toast('error', `连接失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast, reopenInPlace]
  )

  // ---- 档案 / 快捷命令 / 高亮 / 主题 ----
  const saveProfile = useCallback(
    async (p: ConnectionProfile) => {
      try {
        const saved = await window.bastion.saveProfile(p)
        usePrefsStore.getState().upsertProfile(saved)
      } catch (err) {
        toast('error', `保存档案失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const deleteProfile = useCallback(
    async (id: string) => {
      try {
        await window.bastion.deleteProfile(id)
        usePrefsStore.getState().removeProfile(id)
      } catch (err) {
        toast('error', `删除档案失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const saveQc = useCallback(
    async (cmd: Parameters<typeof window.bastion.saveQuickCommand>[0]) => {
      try {
        const saved = await window.bastion.saveQuickCommand(cmd)
        usePrefsStore.getState().upsertQuickCommand(saved)
      } catch (err) {
        toast('error', `保存快捷命令失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const deleteQc = useCallback(
    async (id: string) => {
      try {
        await window.bastion.deleteQuickCommand(id)
        usePrefsStore.getState().removeQuickCommand(id)
      } catch (err) {
        toast('error', `删除快捷命令失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const sendQc = useCallback((command: string) => {
    const id = useTabsStore.getState().activeTabId
    if (!id) return
    window.bastion.sendCommand(id, command)
  }, [])

  const saveHl = useCallback(
    async (rule: Parameters<typeof window.bastion.saveHighlightRule>[0]) => {
      try {
        const saved = await window.bastion.saveHighlightRule(rule)
        usePrefsStore.getState().upsertHighlightRule(saved)
      } catch (err) {
        toast('error', `保存高亮规则失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const deleteHl = useCallback(
    async (id: string) => {
      try {
        await window.bastion.deleteHighlightRule(id)
        usePrefsStore.getState().removeHighlightRule(id)
      } catch (err) {
        toast('error', `删除高亮规则失败: ${err instanceof Error ? err.message : String(err)}`)
      }
    },
    [toast]
  )

  const importTheme = useCallback(async () => {
    try {
      const t = await window.bastion.importTheme()
      if (t) {
        usePrefsStore.getState().setTheme(t)
        toast('success', `主题已应用：${t.name}`)
      }
    } catch (err) {
      toast('error', err instanceof Error ? err.message : String(err))
    }
  }, [toast])

  const resetTheme = useCallback(async () => {
    try {
      const t = await window.bastion.resetTheme()
      usePrefsStore.getState().setTheme(t)
      toast('success', '已恢复默认主题（白天）')
    } catch (err) {
      toast('error', err instanceof Error ? err.message : String(err))
    }
  }, [toast])

  const darkTheme = useCallback(async () => {
    try {
      const t = await window.bastion.builtinDarkTheme()
      usePrefsStore.getState().setTheme(t)
      toast('success', '已切换内置暗色主题')
    } catch (err) {
      toast('error', err instanceof Error ? err.message : String(err))
    }
  }, [toast])

  // ---- 批量连接 ----
  const autoSelectInMenu = useCallback(async (sessionId: string, host: string) => {
    await new Promise<void>((resolve) => {
      let done = false
      const off = window.bastion.onSessionData(({ sessionId: sid }) => {
        if (sid !== sessionId || done) return
        done = true
        off()
        resolve()
      })
      setTimeout(() => {
        if (!done) {
          done = true
          off()
          resolve()
        }
      }, 3000)
    })
    await new Promise<void>((r) => setTimeout(r, 800))
    window.bastion.writeSession(sessionId, `${host}\r`)
  }, [])

  const runBatch = useCallback(
    async (conn: ConnectionInfo, opts: { hosts: string[]; autoSelect: boolean; autoBroadcast: boolean }) => {
      toast('info', `批量连接：开始创建 ${opts.hosts.length} 个会话（复用已认证连接，免 MFA）`)
      let ok = 0
      let failed = 0
      let firstSessionId: string | null = null
      for (const host of opts.hosts) {
        try {
          const { sessionId } = await window.bastion.openSession({ connectionId: conn.id, cols: 100, rows: 30 })
          useTabsStore.getState().addTab(conn.id, sessionId, host)
          useSplitStore.getState().addSession(sessionId)
          if (!firstSessionId) firstSessionId = sessionId
          if (opts.autoBroadcast) {
            useBroadcastStore.getState().add(sessionId)
          }
          if (opts.autoSelect) {
            void autoSelectInMenu(sessionId, host)
          }
          ok++
          await new Promise<void>((r) => setTimeout(r, 150))
        } catch (err) {
          failed++
          toast('error', `${host} 连接失败: ${err instanceof Error ? err.message : String(err)}`)
        }
      }
      if (firstSessionId) useTabsStore.getState().setActiveTab(firstSessionId)
      if (opts.autoBroadcast && ok > 0) {
        useBroadcastStore.getState().setMaster(true)
      }
      toast(
        ok > 0 ? 'success' : 'error',
        `批量连接完成：成功 ${ok} 台，失败 ${failed} 台` +
          (opts.autoBroadcast && ok > 0 ? '；📡 广播已开启，敲一次命令即群发' : '')
      )
    },
    [toast, autoSelectInMenu]
  )

  // ---- 批量部署：逐台 开会话 → 选机 → 选用户 → 前置命令 → 上传 → 跑脚本 ----
  const runDeploy = useCallback(
    async (
      conn: ConnectionInfo,
      opts: { taskName: string; hosts: string[]; uploads: string[]; preCommand: string; script: string; userChoice: string }
    ) => {
      let ok = 0
      let failed = 0
      const firstSessionIds: string[] = []
      // 初始化进度面板
      setDeployProgress({ taskName: opts.taskName, hosts: opts.hosts.map((h) => ({ host: h, status: 'pending' })) })
      for (let i = 0; i < opts.hosts.length; i++) {
        const host = opts.hosts[i]
        const mark = (status: 'running' | 'done' | 'error'): void => {
          setDeployProgress((p) =>
            p ? { ...p, hosts: p.hosts.map((h, j) => (j === i ? { ...h, status } : h)) } : p
          )
        }
        mark('running')
        try {
          const { sessionId } = await window.bastion.openSession({ connectionId: conn.id, cols: 100, rows: 30 })
          useTabsStore.getState().addTab(conn.id, sessionId, host)
          useSplitStore.getState().addSession(sessionId)
          firstSessionIds.push(sessionId)
          // 过堡垒机菜单选主机
          await autoSelectInMenu(sessionId, host)
          // 堡垒机二级菜单：选择用户（默认 1）
          await new Promise<void>((r) => setTimeout(r, 1500))
          window.bastion.writeSession(sessionId, `${opts.userChoice || '1'}\r`)
          await new Promise<void>((r) => setTimeout(r, 2000))
          // 上传前命令（如 sudo -i，最后一条 cd 到上传目录）
          if (opts.preCommand.trim()) {
            window.bastion.writeSession(sessionId, opts.preCommand.replace(/\r\n/g, '\n') + '\r')
            await new Promise<void>((r) => setTimeout(r, 1500))
          }
          // 上传文件（rz 传到远程当前目录，前置命令里已 cd 过去）
          if (opts.uploads.length > 0) {
            await window.bastion.sendFiles(sessionId, opts.uploads, 'skip')
          }
          // 执行脚本
          if (opts.script.trim()) {
            window.bastion.writeSession(sessionId, opts.script.replace(/\r\n/g, '\n') + '\r')
          }
          ok++
          mark('done')
        } catch (err) {
          failed++
          mark('error')
          toast('error', `${host} 部署失败: ${err instanceof Error ? err.message : String(err)}`)
        }
        await new Promise<void>((r) => setTimeout(r, 400))
      }
      if (firstSessionIds.length > 0) useTabsStore.getState().setActiveTab(firstSessionIds[0])
      toast(ok > 0 ? 'success' : 'error', `部署完成：成功 ${ok} 台，失败 ${failed} 台`)
      // 保留进度面板几秒后再收起
      setTimeout(() => setDeployProgress(null), 8000)
    },
    [toast, autoSelectInMenu]
  )

  // ---- 批量执行：逐个执行勾选的任务（在当前部署页绑定的连接上）----
  const runBatchExecute = useCallback(
    async (conn: ConnectionInfo, tasks: DeployTask[]) => {
      for (const task of tasks) {
        toast('info', `开始执行「${task.name || '未命名'}」`)
        await runDeploy(conn, {
          taskName: task.name || '未命名',
          hosts: task.hosts,
          uploads: task.uploads ?? [],
          preCommand: task.preCommand ?? '',
          script: task.script,
          userChoice: task.userChoice ?? '1'
        })
      }
    },
    [runDeploy, toast]
  )

  // ---- 诊断 / 断开 ----
  const runDiagnoseFor = useCallback(
    async (connectionId: string) => {
      setDiagnoseMsg('诊断中…（尝试在同一连接上打开第二个通道）')
      try {
        const r = await window.bastion.diagnose(connectionId)
        const text = r.ok
          ? `复用诊断通过：第二通道打开成功，收到 ${r.bytesReceived} 字节数据 → 堡垒机支持单连接多会话`
          : `复用诊断未通过：${r.message}`
        setDiagnoseMsg(text)
        toast(r.ok ? 'success' : 'error', text)
      } catch (err) {
        const text = `诊断异常: ${err instanceof Error ? err.message : String(err)}`
        setDiagnoseMsg(text)
        toast('error', text)
      }
      setTimeout(() => setDiagnoseMsg(null), 10000)
    },
    [toast]
  )

  const disconnect = useCallback((connectionId: string) => {
    userClosedIds.add(connectionId)
    void window.bastion.closeConnection(connectionId)
  }, [])

  const removeCard = useCallback((connectionId: string) => {
    // 移除已断开连接的卡片时，连同其冻结标签与终端实例一起清理
    const sids = useTabsStore
      .getState()
      .tabs.filter((t) => t.connectionId === connectionId)
      .map((t) => t.sessionId)
    for (const sid of sids) {
      getTerminalManager().disposeSession(sid)
      useBroadcastStore.getState().prune(sid)
    }
    useTabsStore.getState().removeTabsOfConnection(connectionId)
    useConnectionsStore.getState().remove(connectionId)
  }, [])

  // ---- 派生 ----
  const activeTab = tabs.find((t) => t.sessionId === activeTabId) ?? null
  const reconnectingConns = connections.filter((c) => c.status === 'reconnecting')
  const titlesMap = new Map(tabs.map((t) => [t.sessionId, t.title]))
  const seqsMap = new Map(tabs.map((t) => [t.sessionId, t.seq]))
  const closedIds = new Set(tabs.filter((t) => t.closed).map((t) => t.sessionId))
  const effectiveBroadcastTargets = broadcastMaster && broadcastIds.length > 1 ? broadcastIds : []
  // 底部状态条派生数据
  const activeConn = activeTab ? (connections.find((c) => c.id === activeTab.connectionId) ?? null) : null
  const activeSize = activeTab ? (termSizes[activeTab.sessionId] ?? null) : null

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-header">
          <h1>BastionShell</h1>
          <button className="btn primary" onClick={() => setShowConnect({ open: true })}>
            ＋ 新建连接
          </button>
        </div>

        <div className="sidebar-tools">
          <button className="sidebar-tool-btn" title="管理快捷命令" onClick={() => setShowQcManager(true)}>
            ⚙ 命令
          </button>
          <button className="sidebar-tool-btn" title="关键词高亮规则" onClick={() => setShowHlManager(true)}>
            🖍 高亮
          </button>
          <button className="sidebar-tool-btn" title="终端主题" onClick={() => setShowTheme(true)}>
            🎨 主题
          </button>
          <button className="sidebar-tool-btn" title="目录同步（rsync 块级增量）" onClick={() => setShowSync(true)}>
            ⇅ 同步
          </button>
        </div>

        {quickCommands.length > 0 && (
          <div className="qc-column">
            {quickCommands.map((q) => (
              <button key={q.id} className="qc-column-btn" title={q.command} onClick={() => sendQc(q.command)}>
                {q.label}
              </button>
            ))}
          </div>
        )}

        <div className="section-title">连接档案</div>
        <ul className="profile-list">
          {profiles.length === 0 && <li className="muted">暂无档案，点「新建连接」创建</li>}
          {profiles.map((p) => (
            <li key={p.id} className="profile-item">
              <div className="profile-main" onClick={() => setShowConnect({ open: true, preset: p })} title="点击连接 / 编辑">
                <div className="profile-name">
                  {p.name}
                  {p.hasStoredPassword && <span title="已在本机加密保存密码"> 🔒</span>}
                </div>
                <div className="profile-sub">
                  {p.username}@{p.host}:{p.port}
                </div>
              </div>
              <button className="icon-btn" title="删除档案" onClick={() => void deleteProfile(p.id)}>
                ✕
              </button>
            </li>
          ))}
        </ul>

        <div className="section-title">连接</div>
        <ul className="connection-list">
          {connections.length === 0 && <li className="muted">暂无连接</li>}
          {connections.map((c) => (
            <li key={c.id} className="connection-item">
              <span className={`dot ${c.status}`} />
              <div className="connection-main">
                <div className="connection-name">
                  {c.profile?.name ?? `${c.username}@${c.host}`}
                  {c.status === 'connected' && (
                    <span className="muted"> · {tabs.filter((t) => t.connectionId === c.id).length} 个会话</span>
                  )}
                </div>
                {c.profile?.name && c.host ? <div className="profile-sub">{c.username}@{c.host}</div> : null}
                <div className="connection-status-text">
                  {statusText(c.status)}
                  {c.status === 'reconnecting' && c.attempt ? `（第 ${c.attempt} 次）` : ''}
                </div>
                <div className="connection-actions">
                  {c.status === 'connected' && (
                    <>
                      <button className="mini-btn" onClick={() => void openSessionFor(c)}>
                        新会话
                      </button>
                      <button className="mini-btn" title="批量连接：粘贴 IP 列表，一次性开多个会话" onClick={() => setBatchTarget(c)}>
                        批量
                      </button>
                      <button className="mini-btn" title="批量部署：上传文件 + 执行脚本" onClick={() => setDeployTarget(c)}>
                        部署
                      </button>
                      <button className="mini-btn" onClick={() => void runDiagnoseFor(c.id)}>
                        诊断
                      </button>
                      <button
                        className="mini-btn"
                        title="端口转发：把服务器上的服务代理到本地"
                        onClick={() => setShowPorts({ connectionId: c.id, profileId: c.profile?.id ?? '' })}
                      >
                        端口
                      </button>
                      <button className="mini-btn danger" onClick={() => disconnect(c.id)}>
                        断开
                      </button>
                    </>
                  )}
                  {c.status === 'reconnecting' && (
                    <button className="mini-btn danger" onClick={() => disconnect(c.id)}>
                      取消自动重连
                    </button>
                  )}
                  {c.status === 'closed' && (
                    <>
                      {c.profile && (
                        <button className="mini-btn" onClick={() => setShowConnect({ open: true, preset: c.profile })}>
                          重连
                        </button>
                      )}
                      <button className="mini-btn danger" onClick={() => removeCard(c.id)}>
                        移除
                      </button>
                    </>
                  )}
                </div>
              </div>
            </li>
          ))}
        </ul>
      </aside>

      <main className="workspace">
        <div className="statusbar">
          <span className="statusbar-left">
            <details className="split-dd">
              <summary className="mini-btn" title="分屏布局：一键重排或合并">
                {tree.kind === 'tree' ? `⊞ 分屏 · ${collectGroupIds(tree).length} 组` : '⊞ 分屏'}
              </summary>
              <div
                className="split-dd-menu"
                onClick={(e) => {
                  if ((e.target as HTMLElement).tagName === 'BUTTON') {
                    ;(e.currentTarget.parentElement as HTMLDetailsElement | null)?.removeAttribute('open')
                  }
                }}
              >
                <button onClick={() => useSplitStore.getState().splitToGrid(1, 2)}>左右 2 分屏</button>
                <button onClick={() => useSplitStore.getState().splitToGrid(2, 1)}>上下 2 分屏</button>
                <button onClick={() => useSplitStore.getState().splitToGrid(2, 2)}>4 分屏（田字）</button>
                {tree.kind === 'tree' && (
                  <button className="danger" onClick={() => useSplitStore.getState().mergeAll()}>
                    合并为单屏
                  </button>
                )}
              </div>
            </details>
            <button
              className="mini-btn"
              title="界面白天/夜间模式切换"
              onClick={() => setUiTheme((v) => (v === 'light' ? 'dark' : 'light'))}
            >
              {uiTheme === 'light' ? '🌙 夜间' : '☀️ 白天'}
            </button>
            <button
              className={`mini-btn bcast-master ${broadcastMaster ? 'on' : ''}`}
              title="广播总开关：打开时输入发往所有标记了 📡 的会话"
              onClick={() => useBroadcastStore.getState().toggleMaster()}
            >
              📡 广播 {broadcastMaster ? '开' : '关'}
            </button>
            <button
              className="mini-btn"
              title="强制刷新所有终端尺寸（分屏后若画面被压缩/错位，点此恢复）"
              onClick={() => getTerminalManager().fitAll()}
            >
              ⟳ 刷新
            </button>
            {broadcastMaster && (
              <>
                <span className="bcast-count">子集 {broadcastIds.length} 台</span>
                <button className="mini-btn" onClick={() => useBroadcastStore.getState().selectAll(tabs.map((t) => t.sessionId))}>
                  全选
                </button>
                <button className="mini-btn" onClick={() => useBroadcastStore.getState().clear()}>
                  清空
                </button>
              </>
            )}
          </span>
          <span className="statusbar-main">
            {reconnectingConns.length > 0 ? (
              <span className="reconnect-banner">
                {reconnectingConns
                  .map((c) => `${c.username}@${c.host} 掉线，正在自动重连${c.attempt ? `（第 ${c.attempt} 次）` : '…'}`)
                  .join('　')}
                <button className="mini-btn danger" onClick={() => reconnectingConns.forEach((c) => disconnect(c.id))}>
                  取消自动重连
                </button>
              </span>
            ) : diagnoseMsg ? (
              <span className="diagnose">{diagnoseMsg}</span>
            ) : (
              <span className="muted">
                分屏 = 编辑组：新会话落在聚焦组；拖组内标签到边缘劈新组、到别的组=移入；组空自动收起。
              </span>
            )}
          </span>
        </div>

        <div className="terminals">
          <SplitPane
            node={tree}
            groups={groups}
            titles={titlesMap}
            seqs={seqsMap}
            closedIds={closedIds}
            activeSessionId={activeTabId}
            focusedGroupId={focusedGroupId}
            broadcastIds={broadcastIds}
            broadcastTargets={effectiveBroadcastTargets}
            highlightRules={highlightRules}
            theme={theme}
            onFocusGroup={(gid) => useSplitStore.getState().focusGroup(gid)}
            onActivate={(gid, sid) => useSplitStore.getState().activateSession(gid, sid)}
            onCloseTab={(sid) => {
              getTerminalManager().disposeSession(sid)
              useTabsStore.getState().closeTab(sid)
            }}
            onToggleBroadcast={(sid) => useBroadcastStore.getState().toggle(sid)}
            onReorder={(gid, sid, idx) => useSplitStore.getState().reorderTabs(gid, sid, idx)}
            onMoveSession={(sid, gid) => useSplitStore.getState().moveSessionTo(sid, gid)}
            onSplitSession={(gid, side, sid) => useSplitStore.getState().splitWithSession(gid, side, sid)}
            onSplitTab={(side, sid) => {
              const g = findGroupOfSession(groups, sid)
              if (g) useSplitStore.getState().splitWithSession(g, side, sid)
            }}
            onReopen={requestReopen}
            onResize={(treeId, ratio) => useSplitStore.getState().resize(treeId, ratio)}
          />
          {tabs.length === 0 && (
            <div className="empty-state">
              <div>还没有会话</div>
              <p>
                在左侧「新建连接」填好堡垒机地址，输入密码 + MFA 完成认证；
                之后新建/复制/批量会话将直接复用这条已认证连接，不再要求 MFA。
              </p>
            </div>
          )}
        </div>

        <BroadcastBar visible={broadcastMaster} targets={broadcastIds} />

        <footer className="statusbar-bottom">
          <span className="sb-left">
            {activeTab ? (
              <>
                <span className="tab-seq">#{activeTab.seq}</span>
                <span className="sb-title" title={activeTab.title}>
                  {activeTab.title}
                </span>
                {activeTab.closed && <span className="sb-closed">⛔ 已断开 · 画面冻结</span>}
              </>
            ) : (
              <span className="muted">无会话</span>
            )}
          </span>
          <span className="sb-right">
            {broadcastMaster && <span>📡 广播 {broadcastIds.length} 台</span>}
            {activeConn && <span>{statusText(activeConn.status)}</span>}
            {activeSize && (
              <span title="当前终端字符尺寸（行×列）">终端 {activeSize.rows}×{activeSize.cols}</span>
            )}
            <button
              className="sb-upload-toggle"
              title={`拖拽上传方式：${OVERWRITE_HINT[uploadOverwrite]}（点击切换）`}
              onClick={() => useUploadStore.getState().cycleOverwrite()}
            >
              ⬆ 上传：{OVERWRITE_LABEL[uploadOverwrite]}
            </button>
            <span title="打开的会话标签总数">{tabs.length} 个会话</span>
          </span>
        </footer>
      </main>

      {showConnect.open && (
        <ConnectDialog
          preset={showConnect.preset}
          onCancel={() => {
            // 取消连接弹窗时必须清掉"原窗口重开"标记，否则下次新建连接会被误当成重开，夺舍老窗口
            reopenForRef.current = null
            setShowConnect({ open: false })
          }}
          onConnect={(p, opts) => {
            setShowConnect({ open: false })
            if (opts.save) void saveProfile(p)
            const reopenFor = reopenForRef.current
            reopenForRef.current = null
            void connect(p, opts, reopenFor)
          }}
        />
      )}

      {authPrompt && <AuthPromptDialog prompt={authPrompt} onSubmit={submitAuth} onCancel={cancelAuth} />}

      {showQcManager && (
        <QuickCommandsDialog
          commands={quickCommands}
          onSave={saveQc}
          onDelete={deleteQc}
          onClose={() => setShowQcManager(false)}
        />
      )}

      {showHlManager && (
        <HighlightRulesDialog
          rules={highlightRules}
          onSave={saveHl}
          onDelete={deleteHl}
          onClose={() => setShowHlManager(false)}
        />
      )}

      {showTheme && (
        <ThemeDialog
          theme={theme}
          onImport={() => void importTheme()}
          onReset={() => void resetTheme()}
          onDark={() => void darkTheme()}
          onClose={() => setShowTheme(false)}
        />
      )}

      {showSync && activeTab && (
        <SyncDialog
          sessionId={activeTab.sessionId}
          onClose={() => setShowSync(false)}
          onDone={(ok, message) => toast(ok ? 'success' : 'error', message)}
        />
      )}

      {showPorts && (
        <PortsPanel
          profileId={showPorts.profileId}
          connectionId={showPorts.connectionId}
          onClose={() => setShowPorts(null)}
        />
      )}

      {deployTarget && (
        <DeployDialog
          profileId={deployTarget.profile?.id ?? ''}
          connectionLabel={deployTarget.profile?.name ?? `${deployTarget.username}@${deployTarget.host}`}
          onExecute={(opts) => {
            const conn = deployTarget
            setDeployTarget(null)
            void runDeploy(conn, opts)
          }}
          onBatchExecute={(tasks) => void runBatchExecute(deployTarget, tasks)}
          onClose={() => setDeployTarget(null)}
          onToast={toast}
        />
      )}

      {batchTarget && (
        <BatchConnectDialog
          hostLabel={`${batchTarget.username}@${batchTarget.host}`}
          onCancel={() => setBatchTarget(null)}
          onStart={(opts) => {
            setBatchTarget(null)
            void runBatch(batchTarget, opts)
          }}
        />
      )}

      {transfers.length > 0 && (
        <div className="transfer-panel">
          {transfers.map((t) => {
            const pct = t.total > 0 ? Math.min(100, Math.round((t.sent / t.total) * 100)) : null
            return (
              <div key={t.id} className="transfer-item">
                <div className="transfer-head">
                  <span className="transfer-name" title={t.name}>
                    {t.direction === 'send' ? '⬆' : '⬇'} {t.name}
                  </span>
                  <button
                    className="mini-btn danger"
                    title="取消传输"
                    onClick={() => window.bastion.cancelTransfer(t.sessionId)}
                  >
                    取消
                  </button>
                </div>
                <div className="transfer-bar">
                  <div className="transfer-fill" style={{ width: `${pct ?? 100}%` }} />
                </div>
                <div className="transfer-stats">
                  {pct !== null ? `${pct}% · ${t.sent} / ${t.total} B` : '传输中…'}
                </div>
              </div>
            )
          })}
        </div>
      )}

      {deployProgress && (
        <div className="deploy-progress-panel">
          <div className="deploy-progress-head">
            <span className="deploy-progress-title">🚀 {deployProgress.taskName}</span>
            <button className="icon-btn" title="收起" onClick={() => setDeployProgress(null)}>✕</button>
          </div>
          <ul className="deploy-progress-list">
            {deployProgress.hosts.map((h) => (
              <li key={h.host} className={`deploy-progress-item ${h.status}`}>
                <span className="deploy-progress-icon">
                  {h.status === 'done' ? '✓' : h.status === 'running' ? '⟳' : h.status === 'error' ? '✗' : '⏳'}
                </span>
                <span className="deploy-progress-host">{h.host}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="toasts">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`}>
            {t.text}
          </div>
        ))}
      </div>
    </div>
  )
}
