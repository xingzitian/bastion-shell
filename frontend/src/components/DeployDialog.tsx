import { useCallback, useEffect, useState } from 'react'
import type { DeployTask } from '../shared/types'
import { loadTasks, persistTasks, newId } from '../lib/deployTasks'

function blankTask(profileId: string): DeployTask {
  return { id: newId(), name: '', profileId, hosts: [], uploads: [], preCommand: '', script: '', userChoice: '1' }
}

function baseName(p: string): string {
  return p.split(/[\\/]/).pop() || p
}

interface Props {
  /** 堡垒机档案 id（任务归属） */
  profileId: string
  /** 堡垒机显示名 */
  connectionLabel: string
  onExecute: (opts: {
    taskName: string
    hosts: string[]
    uploads: string[]
    preCommand: string
    script: string
    userChoice: string
  }) => void
  /** 批量执行勾选的任务（在部署页内） */
  onBatchExecute: (tasks: DeployTask[]) => void
  onClose: () => void
  onToast: (kind: 'info' | 'error' | 'success', text: string) => void
}

export function DeployDialog({ profileId, connectionLabel, onExecute, onBatchExecute, onClose, onToast }: Props) {
  const [tasks, setTasks] = useState<DeployTask[]>(loadTasks)
  const [current, setCurrent] = useState<DeployTask>(() => blankTask(profileId))
  const [hostsText, setHostsText] = useState('')
  const [preCommand, setPreCommand] = useState('')
  const [script, setScript] = useState('')
  const [uploads, setUploads] = useState<string[]>([])
  const [taskName, setTaskName] = useState('')
  const [userChoice, setUserChoice] = useState('1')
  const [busy, setBusy] = useState(false)
  const [checkedIds, setCheckedIds] = useState<Set<string>>(new Set())

  // 打开时自动载入该堡垒机最近一次任务
  useEffect(() => {
    const mine = loadTasks().filter((t) => t.profileId === profileId)
    if (mine.length > 0) {
      const latest = mine[mine.length - 1]
      setCurrent(latest)
      setTaskName(latest.name === '（未命名任务）' ? '' : latest.name)
      setHostsText(latest.hosts.join('\n'))
      setUploads(latest.uploads ?? [])
      setPreCommand(latest.preCommand ?? '')
      setScript(latest.script)
      setUserChoice(latest.userChoice ?? '1')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const addFiles = useCallback(async () => {
    try {
      const paths = await window.bastion.pickFiles()
      if (paths.length === 0) return
      setUploads([...uploads, ...paths])
    } catch (err) {
      onToast('error', `选择文件失败: ${err instanceof Error ? err.message : String(err)}`)
    }
  }, [uploads, onToast])

  const saveTask = useCallback(() => {
    if (!taskName.trim()) {
      onToast('error', '请先给任务起个名字')
      return
    }
    const task: DeployTask = {
      id: current.id,
      name: taskName.trim(),
      profileId,
      hosts: hostsText.split(/\r?\n/).map((s) => s.trim()).filter(Boolean),
      uploads,
      preCommand,
      script,
      userChoice
    }
    setTasks((prev) => {
      const i = prev.findIndex((t) => t.id === task.id)
      const next = i >= 0 ? prev.map((t) => (t.id === task.id ? task : t)) : [...prev, task]
      persistTasks(next)
      return next
    })
    setCurrent(task)
    onToast('success', '任务已保存')
  }, [taskName, current.id, profileId, hostsText, uploads, preCommand, script, userChoice, onToast])

  const loadTask = useCallback((t: DeployTask) => {
    setCurrent(t)
    setTaskName(t.name === '（未命名任务）' ? '' : t.name)
    setHostsText(t.hosts.join('\n'))
    setUploads(t.uploads ?? [])
    setPreCommand(t.preCommand ?? '')
    setScript(t.script)
    setUserChoice(t.userChoice ?? '1')
  }, [])

  const deleteTask = useCallback((id: string) => {
    setTasks((prev) => {
      const next = prev.filter((t) => t.id !== id)
      persistTasks(next)
      return next
    })
  }, [])

  const toggleChecked = useCallback((id: string) => {
    setCheckedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  const runBatch = useCallback(() => {
    const chosen = tasks.filter((t) => t.profileId === profileId && checkedIds.has(t.id))
    if (chosen.length === 0) {
      onToast('error', '请先勾选要批量执行的任务')
      return
    }
    onBatchExecute(chosen)
  }, [tasks, profileId, checkedIds, onBatchExecute, onToast])

  const execute = useCallback(() => {
    const hosts = hostsText.split(/\r?\n/).map((s) => s.trim()).filter(Boolean)
    if (hosts.length === 0) {
      onToast('error', '请至少填一台目标机器（一行一个）')
      return
    }
    if (uploads.length === 0 && !script.trim()) {
      onToast('error', '请至少添加一个文件或写一条脚本')
      return
    }
    // 自动保存当前配置，方便下次复用
    const task: DeployTask = {
      id: current.id,
      name: taskName.trim() || '（未命名任务）',
      profileId,
      hosts,
      uploads,
      preCommand,
      script,
      userChoice
    }
    setCurrent(task)
    setTasks((prev) => {
      const i = prev.findIndex((t) => t.id === task.id)
      const next = i >= 0 ? prev.map((t) => (t.id === task.id ? task : t)) : [...prev, task]
      persistTasks(next)
      return next
    })
    onExecute({ taskName: task.name, hosts, uploads, preCommand, script, userChoice })
  }, [hostsText, uploads, preCommand, script, userChoice, taskName, profileId, current.id, onExecute, onToast])

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        style={{ width: '820px', maxWidth: 'calc(100vw - 40px)', maxHeight: 'calc(100vh - 60px)' }}
        onClick={(e) => e.stopPropagation()}
      >
        <h2>🚀 部署 · {connectionLabel}</h2>

        <div className="deploy-layout">
          <div className="deploy-tasks">
            <div className="deploy-tasks-head">
              <span className="muted">已存任务</span>
              <button className="mini-btn" onClick={saveTask} title="保存当前任务">💾 保存</button>
            </div>
            <ul className="deploy-task-list">
              {tasks.filter((t) => t.profileId === profileId).length === 0 && <li className="muted">暂无任务</li>}
              {tasks
                .filter((t) => t.profileId === profileId)
                .map((t) => (
                  <li key={t.id} className={`deploy-task-item ${t.id === current.id ? 'active' : ''}`}>
                    <input type="checkbox" checked={checkedIds.has(t.id)} onChange={() => toggleChecked(t.id)} />
                    <div className="deploy-task-name" onClick={() => loadTask(t)} title={t.name}>
                      {t.name || '（未命名）'}
                    </div>
                    <button className="icon-btn" title="删除任务" onClick={() => deleteTask(t.id)}>✕</button>
                  </li>
                ))}
            </ul>
            <button className="mini-btn" onClick={runBatch} disabled={busy}>
              ▶ 批量执行选中（{checkedIds.size}）
            </button>
          </div>

          <div className="deploy-editor">
            <div className="field">
              <label className="field-label">任务名称</label>
              <input type="text" value={taskName} placeholder="例如：部署 dcgm 监控配置" onChange={(e) => setTaskName(e.target.value)} />
            </div>

            <div className="field">
              <label className="field-label">目标机器（一行一个 host，过堡垒机菜单批量连接）</label>
              <textarea
                className="deploy-script"
                value={hostsText}
                placeholder={'10.0.0.1\n10.0.0.2'}
                onChange={(e) => setHostsText(e.target.value)}
                style={{ minHeight: 64 }}
              />
            </div>

            <div className="field">
              <label className="field-label">选择用户序号（堡垒机二级菜单，默认 1）</label>
              <input type="text" value={userChoice} onChange={(e) => setUserChoice(e.target.value)} />
            </div>

            <div className="field">
              <label className="field-label">上传前执行的命令（如 sudo -i；最后一条命令 cd 到上传目录）</label>
              <textarea
                className="deploy-script"
                value={preCommand}
                placeholder={'sudo -i\ncd /etc'}
                onChange={(e) => setPreCommand(e.target.value)}
                style={{ minHeight: 56 }}
              />
            </div>

            <div className="field">
              <label className="field-label">上传文件列表（会逐个上传到远程当前目录）</label>
              <button className="mini-btn" onClick={() => void addFiles()} disabled={busy}>＋ 添加文件</button>
              {uploads.length === 0 && <div className="muted">尚未添加文件</div>}
              {uploads.map((p, i) => (
                <div key={i} className="deploy-upload-row">
                  <span className="deploy-local" title={p}>{baseName(p)}</span>
                  <button className="icon-btn" title="移除" onClick={() => setUploads(uploads.filter((_, k) => k !== i))}>✕</button>
                </div>
              ))}
            </div>

            <div className="field">
              <label className="field-label">执行脚本（多行 shell，上传完成后执行）</label>
              <textarea
                className="deploy-script"
                value={script}
                placeholder={'echo done'}
                onChange={(e) => setScript(e.target.value)}
              />
            </div>
          </div>
        </div>

        <div className="modal-actions">
          <span className="muted">连接 → 选机 → 选用户 → 前置命令 → 上传 → 跑脚本</span>
          <button className="btn" onClick={onClose}>关闭</button>
          <button className="btn primary" onClick={execute} disabled={busy}>
            {busy ? '执行中…' : '▶ 执行'}
          </button>
        </div>
      </div>
    </div>
  )
}
