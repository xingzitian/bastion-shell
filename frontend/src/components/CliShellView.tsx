import { useEffect, useRef, useState } from 'react'

/**
 * CLI 薄壳（B2，beta）：把 CLI 宿主的**网页终端**装进桌面窗口。
 *
 * 为什么是 iframe 而不是 Wails 绑定：
 *   - 终端渲染、WebSocket、MCP 全在 CLI 进程里（`/term`），桌面只负责"装进去"；
 *   - 后端 `/api/shell/cli?profile=…` 会 **302 到带 token 的 /term 地址**，
 *     浏览器/WebView 自己跟随，桌面不需要知道 token。
 *
 * 关闭时顺手 `POST /api/shell/cli/stop`，别留孤儿 node 进程。
 */

const API_BASE = 'http://127.0.0.1:18090'

export function CliShellView({ profile, onClose }: { profile: string; onClose: () => void }) {
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const url = `${API_BASE}/api/shell/cli?profile=${encodeURIComponent(profile)}`
  const timer = useRef<number | null>(null)

  useEffect(() => {
    // 15s 还没加载出来就提示（CLI 连服务器通常几秒内）
    timer.current = window.setTimeout(() => setLoading(false), 15000)
    return () => {
      if (timer.current) window.clearTimeout(timer.current)
    }
  }, [])

  // 先探一下：后端返回 JSON error 时要显示原因（iframe 里只会是一坨 JSON）
  useEffect(() => {
    let cancelled = false
    void fetch(url, { redirect: 'manual' })
      .then((r) => {
        if (cancelled) return
        if (r.type === 'opaqueredirect' || r.status === 302) {
          setLoading(false)
          return
        }
        // 非 302：说明是错误 JSON
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
    }
  }, [url])

  const close = (): void => {
    void fetch(`${API_BASE}/api/shell/cli/stop`, { method: 'POST' }).catch(() => undefined)
    onClose()
  }

  return (
    <div className="cli-shell-overlay" data-testid="cli-shell">
      <div className="cli-shell-bar">
        <span>
          <b>CLI 终端</b>（beta）· 档案 {profile}
        </span>
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
            需要 Node ≥ 22，且设置了 <code>BASTIONSHELL_CLI_JS</code> 指向{' '}
            <code>&lt;仓库&gt;/out/cli/main.js</code>。
          </p>
        </div>
      ) : (
        <iframe className="cli-shell-frame" src={url} title={`CLI 终端 ${profile}`} />
      )}
      {loading && !error ? <div className="cli-shell-loading">正在启动 CLI 宿主…</div> : null}
    </div>
  )
}
