/**
 * 渲染层事件总线（种子版）：组件与全局能力（toast/状态栏等）解耦的管道。
 * 后续插件扩展点、诊断面板都挂在这上面。
 */
export interface BusEvents {
  'ui:toast': { kind: 'info' | 'error' | 'success'; text: string }
  /** 连接关闭（供 App 清理认证提示弹窗） */
  'conn:closed': { connectionId: string }
}

type Handler<T> = (payload: T) => void

const handlers = new Map<keyof BusEvents, Set<Handler<never>>>()

export const bus = {
  on<K extends keyof BusEvents>(topic: K, h: Handler<BusEvents[K]>): () => void {
    let set = handlers.get(topic)
    if (!set) {
      set = new Set()
      handlers.set(topic, set as Set<Handler<never>>)
    }
    ;(set as Set<Handler<BusEvents[K]>>).add(h)
    return () => {
      ;(set as Set<Handler<BusEvents[K]>>).delete(h)
    }
  },
  emit<K extends keyof BusEvents>(topic: K, payload: BusEvents[K]): void {
    const set = handlers.get(topic)
    if (set) {
      for (const h of [...set]) (h as Handler<BusEvents[K]>)(payload)
    }
  }
}
