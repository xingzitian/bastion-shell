import { create } from 'zustand'
import type { PortForwardRule, PortForwardStatus } from '../shared/types'

/** 端口转发状态（面板解耦：面板只读此 store，事件由 subscriptions 集中写入） */
interface ForwardState {
  rules: PortForwardRule[]
  statuses: Record<string, PortForwardStatus>
  messages: Record<string, string>
  setRules: (rules: PortForwardRule[]) => void
  upsertRule: (rule: PortForwardRule) => void
  removeRule: (ruleId: string) => void
  setStatus: (ruleId: string, status: PortForwardStatus, message?: string) => void
}

export const useForwardStore = create<ForwardState>()((set) => ({
  rules: [],
  statuses: {},
  messages: {},
  setRules: (rules) => set({ rules }),
  upsertRule: (rule) => set((s) => ({ rules: [...s.rules.filter((r) => r.id !== rule.id), rule] })),
  removeRule: (ruleId) =>
    set((s) => ({
      rules: s.rules.filter((r) => r.id !== ruleId),
      statuses: { ...s.statuses, [ruleId]: 'idle' as PortForwardStatus }
    })),
  setStatus: (ruleId, status, message) =>
    set((s) => ({
      statuses: { ...s.statuses, [ruleId]: status },
      messages: { ...s.messages, [ruleId]: message ?? '' }
    }))
}))
