import type { TerminalTheme } from '../shared/types'

interface Props {
  theme: TerminalTheme
  onImport: () => void
  onReset: () => void
  onDark: () => void
  onClose: () => void
}

const ANSI_LABELS = ['黑', '红', '绿', '黄', '蓝', '品红', '青', '白', '亮黑', '亮红', '亮绿', '亮黄', '亮蓝', '亮品红', '亮青', '亮白']

/** 主题管理弹窗：当前主题预览 + 导入 VSCode 主题 JSON + 恢复默认 */
export function ThemeDialog({ theme, onImport, onReset, onDark, onClose }: Props) {
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal theme-modal" onClick={(e) => e.stopPropagation()}>
        <h2>终端主题</h2>
        <div className="theme-current">
          当前：<b>{theme.name}</b>
        </div>
        <div className="theme-swatches">
          <div className="theme-swatch-row">
            <span className="theme-swatch" style={{ background: theme.background, color: theme.foreground, border: '1px solid #444' }}>
              背景/前景
            </span>
            <span className="theme-swatch" style={{ background: theme.foreground, color: theme.background }}>
              前景
            </span>
            <span className="theme-swatch" style={{ background: theme.cursor, color: '#000' }}>
              光标
            </span>
            <span className="theme-swatch" style={{ background: theme.selectionBackground, color: theme.foreground }}>
              选区
            </span>
          </div>
          <div className="theme-swatch-row">
            {theme.ansi.map((c, i) => (
              <span key={i} className="theme-swatch" style={{ background: c }} title={ANSI_LABELS[i]}>
                {ANSI_LABELS[i]}
              </span>
            ))}
          </div>
        </div>
        <div className="muted" style={{ fontSize: 12 }}>
          导入 VSCode 颜色主题的 JSON 文件（themes/*.json），自动提取 terminal.* 配色；主题文件位于
          ~/.vscode/extensions/&lt;主题包&gt;/themes/ 下。
        </div>
        <div className="modal-actions">
          <button className="btn" onClick={onDark}>
            🌙 内置暗色
          </button>
          <button className="btn" onClick={onReset}>
            ☀️ 恢复默认（白天）
          </button>
          <span style={{ flex: 1 }} />
          <button className="btn primary" onClick={onImport}>
            📂 导入 VSCode 主题 JSON…
          </button>
          <button className="btn" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
