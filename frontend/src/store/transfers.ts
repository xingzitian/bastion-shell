import { create } from 'zustand'
import type { ZmodemEventPayload } from '../shared/types'

export interface Transfer {
  id: string
  sessionId: string
  name: string
  direction: 'send' | 'receive'
  sent: number
  total: number
}

interface TransfersState {
  transfers: Transfer[]
  start: (p: ZmodemEventPayload) => void
  progress: (id: string, sent?: number) => void
  remove: (id: string) => void
}

export const useTransfersStore = create<TransfersState>()((set) => ({
  transfers: [],
  start: (p) =>
    set((s) => ({
      transfers: [
        ...s.transfers.filter((x) => x.id !== p.transferId),
        { id: p.transferId, sessionId: p.sessionId, name: p.name ?? '', direction: p.direction, sent: 0, total: p.bytesTotal ?? 0 }
      ]
    })),
  progress: (id, sent) =>
    set((s) => ({
      transfers: s.transfers.map((x) => (x.id === id && sent !== undefined ? { ...x, sent } : x))
    })),
  remove: (id) => set((s) => ({ transfers: s.transfers.filter((x) => x.id !== id) }))
}))
