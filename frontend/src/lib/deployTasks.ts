import type { DeployTask } from '../shared/types'

const LS_KEY = 'bastion-shell-deploy-tasks'

/** 兼容旧数据：早期 uploads 存的是 {localPath,remotePath} 对象，现在存字符串路径 */
function normalizeTask(t: any): DeployTask {
  const uploads = Array.isArray(t?.uploads)
    ? t.uploads
        .map((u: any) => (typeof u === 'string' ? u : u?.localPath ?? ''))
        .filter((p: any) => typeof p === 'string' && p.length > 0)
    : []
  return {
    id: typeof t?.id === 'string' ? t.id : newId(),
    name: typeof t?.name === 'string' ? t.name : '',
    profileId: typeof t?.profileId === 'string' ? t.profileId : '',
    hosts: Array.isArray(t?.hosts) ? t.hosts.filter((h: any) => typeof h === 'string') : [],
    uploads,
    preCommand: typeof t?.preCommand === 'string' ? t.preCommand : '',
    script: typeof t?.script === 'string' ? t.script : '',
    userChoice: typeof t?.userChoice === 'string' ? t.userChoice : '1'
  }
}

export function loadTasks(): DeployTask[] {
  try {
    const v = localStorage.getItem(LS_KEY)
    const arr = v ? JSON.parse(v) : []
    return (Array.isArray(arr) ? arr : []).map(normalizeTask)
  } catch {
    return []
  }
}

export function persistTasks(tasks: DeployTask[]): void {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(tasks))
  } catch {
    /* ignore */
  }
}

export function newId(): string {
  return `deploy-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}
