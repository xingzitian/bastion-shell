import { useState } from 'react'
import type { HighlightColor, HighlightRule } from '../shared/types'
import { HIGHLIGHT_COLORS } from '../lib/highlight'

interface Props {
  rules: HighlightRule[]
  onSave: (rule: HighlightRule) => Promise<void>
  onDelete: (id: string) => Promise<void>
  onClose: () => void
}

const COLORS: HighlightColor[] = ['red', 'yellow', 'green', 'blue', 'orange', 'purple']

const COLOR_LABELS: Record<HighlightColor, string> = {
  red: '红',
  yellow: '黄',
  green: '绿',
  blue: '蓝',
  orange: '橙',
  purple: '紫'
}

/** 关键词高亮规则管理弹窗 */
export function HighlightRulesDialog({ rules, onSave, onDelete, onClose }: Props) {
  const [rows, setRows] = useState<HighlightRule[]>(() => rules.map((r) => ({ ...r })))

  const update = (id: string, patch: Partial<HighlightRule>) => {
    setRows((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }

  const add = () => {
    setRows((rs) => [...rs, { id: crypto.randomUUID(), pattern: '', color: 'yellow', enabled: true }])
  }

  const validate = (r: HighlightRule): string | null => {
    if (!r.pattern.trim()) return '模式不能为空'
    try {
      new RegExp(r.pattern)
    } catch {
      return '正则表达式无效'
    }
    return null
  }

  const save = (r: HighlightRule) => {
    const err = validate(r)
    if (err) return
    void onSave({ ...r })
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal qc-modal" onClick={(e) => e.stopPropagation()}>
        <h2>关键词高亮规则</h2>
        <div className="muted" style={{ fontSize: 12 }}>
          正则匹配终端输出并着色（内置 error / warning / "没有资产" / IP 规则，可禁用或删除）
        </div>
        <div className="hl-off-notice">
          ⚠️ 关键词高亮已临时关闭：连续日志输出时装饰渲染会出现错位与跳动，正在换用更稳的方案。规则修改仍会保存，重新开启后生效。
        </div>
        <div className="qc-list">
          {rows.length === 0 && <div className="muted">没有规则，点「＋ 新增」添加</div>}
          {rows.map((r) => (
            <div key={r.id} className="qc-row">
              <input
                type="text"
                className="qc-command"
                style={{ flex: 2 }}
                placeholder="正则表达式（如 \bERROR\b）"
                value={r.pattern}
                onChange={(e) => update(r.id, { pattern: e.target.value })}
              />
              <select
                className="hl-color"
                value={r.color}
                onChange={(e) => update(r.id, { color: e.target.value as HighlightColor })}
                title="高亮颜色"
              >
                {COLORS.map((c) => (
                  <option key={c} value={c}>
                    {COLOR_LABELS[c]}
                  </option>
                ))}
              </select>
              <label className="checkbox-field" title="启用/禁用">
                <input
                  type="checkbox"
                  checked={r.enabled}
                  onChange={(e) => update(r.id, { enabled: e.target.checked })}
                />
                启用
              </label>
              <label className="checkbox-field" title="忽略大小写（英文关键词推荐开启）">
                <input
                  type="checkbox"
                  checked={r.ignoreCase !== false}
                  onChange={(e) => update(r.id, { ignoreCase: e.target.checked })}
                />
                忽略大小写
              </label>
              <button className="mini-btn" onClick={() => save(r)} title="保存">
                保存
              </button>
              <button
                className="icon-btn"
                title="删除"
                onClick={() => void onDelete(r.id).then(() => setRows((rs) => rs.filter((x) => x.id !== r.id)))}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
        <div className="hl-preview">
          <span className="hl-preview-label">配色预览：</span>
          {COLORS.map((c) => (
            <span key={c} style={{ background: HIGHLIGHT_COLORS[c].bg, color: '#e4e4ef', padding: '1px 6px', borderRadius: 3, marginRight: 4 }}>
              {COLOR_LABELS[c]}
            </span>
          ))}
        </div>
        <div className="modal-actions">
          <button className="btn" onClick={add}>
            ＋ 新增
          </button>
          <span style={{ flex: 1 }} />
          <button className="btn" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
