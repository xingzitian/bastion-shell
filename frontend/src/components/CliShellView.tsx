import { useEffect, useRef, useState } from 'react'

/**
 * CLI 薄壳（B2，beta）：把 CLI 宿主的**网页终端**装进桌面窗口。
 *
 * 为什么是 iframe 而不是 Wails 绑定：
 *   - 终端渲染、WebSocket、MCP 全在 CLI 进程里（`/term`），桌面只负责"装进去"；
 *   - 后端 `/api/shell/cli?profile=…` 会 **302 到带 token 的 /term 地址**，
 *     浏览器/WebView 自己跟随，桌面不需要知道 token。
 *
 * 顶栏显示 MCP 端点 + token（可复制）—— 这是外面那批"要用 MCP"的人最直接的诉求：
 * 他能立刻把这段贴进自己的 AI 客户端，不用去翻 mcp.json。
 */

const API_BASE = 'http://127.0.0.1:18090'

interface EndpointInfo {
  running: boolean
  profile?: string
  termUrl?: string
  mcpUrl?: string
  token?: string
  port?: number
  actualPort?: number
  writeError?: string
}

export function CliShellView({ profiles, onClose, directUrl }: { profiles: string[]; onClose: () => void; directUrl?: string }) {
  const profile = profiles.join('+')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [ep, setEp] = useState<EndpointInfo | null>(null)
  const [copied, setCopied] = useState<'url' | 'json' | null>(null)
  // ⚠️ 运算符优先级：`directUrl || len===1 ? a : b` 会被解析成 `(directUrl || len===1) ? a : b`，
  // directUrl 就等于没生效（2026-11-23 读代码时发现）。这里显式分开算。
  const spawnUrl =
    profiles.length === 1
      ? `${API_BASE}/api/shell/cli?profile=${encodeURIComponent(profiles[0])}`
      : `${API_BASE}/api/shell/cli?profiles=${encodeURIComponent(profiles.join(','))}`
  /** iframe 用：POST start 已经拿到 termUrl 就直接用，否则走 GET（会 302 到 term） */
  const url = directUrl ?? spawnUrl
  const timer = useRef<number | null>(null)

  useEffect(() => {
    // 15s 还没加载出来就提示（CLI 连服务器通常几秒内）
    timer.current = window.setTimeout(() => setLoading(false), 15000)
    return () => {
      if (timer.current) window.clearTimeout(timer.current)
    }
  }, [])

  // 先探一下：后端返回 JSON error 时要显示原因（iframe 里只会是一坨 JSON）。
  // 302 就说明起了，接着轮询端点信息（token 只有 /api/shell/endpoint 有）。
  //
  // ⚠️ directUrl 是「POST start 已经起的会话」的 term 地址：这时**不能再打 spawn 路由**，
  // 否则会（a）重复起一个进程、（b）把 term 页面当成错误 JSON 显示 "起不来：<!DOCTYPE html>"。
  useEffect(() => {
    let cancelled = false
    let interval: number | undefined
    const poll = (): void => {
      void fetch(`${API_BASE}/api/shell/endpoint`)
        .then((r) => r.json())
        .then((j: EndpointInfo) => {
          if (cancelled) return
          setEp(j)
          if (j.running && j.termUrl) setLoading(false)
        })
        .catch(() => undefined)
    }

    if (directUrl) {
      poll()
      interval = window.setInterval(poll, 1500)
      window.setTimeout(() => window.clearInterval(interval), 40000)
      return () => {
        cancelled = true
        if (interval) window.clearInterval(interval)
      }
    }

    void fetch(spawnUrl, { redirect: 'manual' })
      .then((r) => {
        if (cancelled) return
        if (r.type === 'opaqueredirect' || r.status === 302) {
          poll()
          interval = window.setInterval(poll, 1500)
          window.setTimeout(() => window.clearInterval(interval), 40000)
          return
        }
        r.text().then((t) => {
          if (cancelled) return
          try {
            const j = JSON.parse(t) as { error?: string }
            setError(j.error ?? t.slice(0, 300))
          } catch {
            setError(t.slice(0, 300))
          }
          setLoading(false)
        })
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setError(`连不上桌面后端：${e instanceof Error ? e.message : String(e)}`)
        setLoading(false)
      })
    return () => {
      cancelled = true
      if (interval) window.clearInterval(interval)
    }
  }, [directUrl, spawnUrl])

  const copy = async (text: string, kind: 'url' | 'json'): Promise<void> => {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      // WebView 里剪贴板可能被拒：退回到 textarea + execCommand
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      try {
        document.execCommand('copy')
      } catch {
        /* 最后没办法就让用户手选 */
      }
      document.body.removeChild(ta)
    }
    setCopied(kind)
    window.setTimeout(() => setCopied(null), 1500)
  }

  const close = (): void => {
    void fetch(`${API_BASE}/api/shell/cli/stop`, { method: 'POST' }).catch(() => undefined)
    onClose()
  }

  const clientCfg = ep?.running && ep.mcpUrl && ep.token ? JSON.stringify({ url: ep.mcpUrl, token: ep.token }) : ''

  return (
    <div className="cli-shell-overlay" data-testid="cli-shell">
      <div className="cli-shell-bar">
        <span>
          <b>CLI 终端</b>（beta）· 档案 {profile}
        </span>
        {ep?.running && ep.mcpUrl ? (
          <button className="link-btn" title="复制 MCP 端点" onClick={() => void copy(ep.mcpUrl!, 'url')}>
            {copied === 'url' ? '✓ 已复制' : `MCP：${ep.mcpUrl}`}
          </button>
        ) : null}
        {clientCfg ? (
          <button className="link-btn" title="复制客户端配置（url + token）" onClick={() => void copy(clientCfg, 'json')}>
            {copied === 'json' ? '✓ 已复制配置' : '复制客户端配置'}
          </button>
        ) : null}
        {ep?.writeError ? (
          <span className="cli-shell-warn" title={ep.writeError}>
            ⚠ 端点文件没写成（终端/ MCP 仍可用）
          </span>
        ) : null}
        {error ? <span className="cli-shell-err">起不来：{error}</span> : null}
        <button className="icon-btn" title="关闭（并收掉 CLI 进程）" onClick={close}>
          ✕
        </button>
      </div>
      {error ? (
        <div className="cli-shell-error">
          <p>CLI 薄壳启动失败：</p>
          <pre>{error}</pre>
          <p className="muted">
            需要 Node ≥ 22，且能找到 CLI 入口（<code>out/cli/main.js</code>）—— 可用{' '}
            <code>BASTIONSHELL_CLI_JS</code> 指定。
          </p>
        </div>
      ) : (
        <iframe className="cli-shell-frame" src={url} title={`CLI 终端 ${profile}`} />
      )}
      {loading && !error ? <div className="cli-shell-loading">正在启动 CLI 宿主…</div> : null}
    </div>
  )
}
