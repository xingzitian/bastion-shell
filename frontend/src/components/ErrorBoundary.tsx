import { Component, ReactNode } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

/** 全局错误边界：任何渲染/effect 异常都显示可读信息而非白屏 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error): void {
    try {
      window.bastion.logToMain('error', `ErrorBoundary: ${error.stack ?? error.message}`)
    } catch {
      /* ignore */
    }
  }

  render(): ReactNode {
    if (this.state.error) {
      return (
        <div className="crash-screen">
          <h2>界面发生错误</h2>
          <pre>{this.state.error.message}</pre>
          <div className="muted" style={{ fontSize: 12 }}>
            错误详情已写入日志（userData/logs/bastion.log），可反馈给开发者定位。
          </div>
          <button className="btn primary" onClick={() => window.location.reload()}>
            重新加载界面
          </button>
        </div>
      )
    }
    return this.props.children
  }
}
