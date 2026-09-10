import { create } from 'zustand'

export type OverwriteMode = 'skip' | 'overwrite' | 'rename'

const KEY = 'bastion-shell-upload-overwrite'

export const OVERWRITE_LABEL: Record<OverwriteMode, string> = {
  skip: '跳过已存在',
  overwrite: '覆盖',
  rename: '重名改名'
}

export const OVERWRITE_HINT: Record<OverwriteMode, string> = {
  skip: 'rz（同名文件不覆盖）',
  overwrite: 'rz -y（直接覆盖同名文件）',
  rename: 'rz -E（已有文件自动改名保留）'
}

function load(): OverwriteMode {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'overwrite' || v === 'rename') return v
  } catch {
    /* ignore */
  }
  return 'skip'
}

interface UploadState {
  overwrite: OverwriteMode
  setOverwrite: (m: OverwriteMode) => void
  /** 循环切换：跳过 → 覆盖 → 改名 → 跳过 */
  cycleOverwrite: () => OverwriteMode
}

/** 上传覆盖模式（持久化到 localStorage，默认保守的"跳过"） */
export const useUploadStore = create<UploadState>()((set, get) => ({
  overwrite: load(),
  setOverwrite: (m) => {
    set({ overwrite: m })
    try {
      localStorage.setItem(KEY, m)
    } catch {
      /* ignore */
    }
  },
  cycleOverwrite: () => {
    const next: OverwriteMode = get().overwrite === 'skip' ? 'overwrite' : get().overwrite === 'overwrite' ? 'rename' : 'skip'
    get().setOverwrite(next)
    return next
  }
}))
