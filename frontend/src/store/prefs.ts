import { create } from 'zustand'
import type { ConnectionProfile, HighlightRule, QuickCommand, TerminalTheme } from '../shared/types'
import { DEFAULT_THEME } from '../shared/types'

interface PrefsState {
  profiles: ConnectionProfile[]
  quickCommands: QuickCommand[]
  highlightRules: HighlightRule[]
  theme: TerminalTheme
  setProfiles: (p: ConnectionProfile[]) => void
  upsertProfile: (p: ConnectionProfile) => void
  removeProfile: (id: string) => void
  setQuickCommands: (q: QuickCommand[]) => void
  upsertQuickCommand: (q: QuickCommand) => void
  removeQuickCommand: (id: string) => void
  setHighlightRules: (r: HighlightRule[]) => void
  upsertHighlightRule: (r: HighlightRule) => void
  removeHighlightRule: (id: string) => void
  setTheme: (t: TerminalTheme) => void
}

export const usePrefsStore = create<PrefsState>()((set) => ({
  profiles: [],
  quickCommands: [],
  highlightRules: [],
  theme: DEFAULT_THEME,

  setProfiles: (p) => set({ profiles: p }),
  upsertProfile: (p) => set((s) => ({ profiles: [...s.profiles.filter((x) => x.id !== p.id), p] })),
  removeProfile: (id) => set((s) => ({ profiles: s.profiles.filter((x) => x.id !== id) })),

  setQuickCommands: (q) => set({ quickCommands: q }),
  upsertQuickCommand: (q) => set((s) => ({ quickCommands: [...s.quickCommands.filter((x) => x.id !== q.id), q] })),
  removeQuickCommand: (id) => set((s) => ({ quickCommands: s.quickCommands.filter((x) => x.id !== id) })),

  setHighlightRules: (r) => set({ highlightRules: r }),
  upsertHighlightRule: (r) => set((s) => ({ highlightRules: [...s.highlightRules.filter((x) => x.id !== r.id), r] })),
  removeHighlightRule: (id) => set((s) => ({ highlightRules: s.highlightRules.filter((x) => x.id !== id) })),

  setTheme: (t) => set({ theme: t })
}))
