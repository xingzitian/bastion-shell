import { create } from 'zustand'
import type { ConnectionProfile, ConnectionStatus } from '../shared/types'

export interface ConnectionInfo {
  id: string
  host: string
  username: string
  profile?: ConnectionProfile
  status: ConnectionStatus
  attempt?: number
}

/** 用户主动断开的连接 id（非响应式集合，订阅处理用它区分主动/意外断开） */
export const userClosedIds = new Set<string>()

interface ConnectionsState {
  connections: ConnectionInfo[]
  upsert: (id: string, patch: Partial<ConnectionInfo>) => void
  remove: (id: string) => void
}

export const useConnectionsStore = create<ConnectionsState>()((set) => ({
  connections: [],
  upsert: (id, patch) =>
    set((s) => {
      const i = s.connections.findIndex((c) => c.id === id)
      if (i >= 0) {
        return { connections: s.connections.map((c, idx) => (idx === i ? { ...c, ...patch } : c)) }
      }
      return {
        connections: [
          ...s.connections,
          { id, host: '', username: '', status: 'connecting' as ConnectionStatus, ...patch }
        ]
      }
    }),
  remove: (id) => set((s) => ({ connections: s.connections.filter((c) => c.id !== id) }))
}))
