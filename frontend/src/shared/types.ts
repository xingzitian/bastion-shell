/** 主进程与渲染进程共享的 IPC 类型定义 */

export interface ConnectionProfile {
  id: string
  name: string
  host: string
  port: number
  username: string
  /** 是否已在本机加密保存密码（密文主进程持有，渲染层只拿此标志） */
  hasStoredPassword?: boolean
  /** 认证方式：password=密码(+MFA)；key=私钥（OpenSSH PEM）。缺省=password */
  authMethod?: 'password' | 'key'
  /** 私钥文件路径（authMethod='key' 时使用） */
  privateKeyPath?: string
  /** 是否已在本机加密保存密钥口令 */
  hasStoredPassphrase?: boolean
}

export interface ConnectRequest {
  profile: ConnectionProfile
  /** 可选：密码快速通道（不持久化，仅保留在内存用于自动重连） */
  password?: string
  /** 使用本机加密保存的密码（主进程解密，密文不外传） */
  useStoredPassword?: boolean
  /** true=保存密码（本机加密）；false=清除已存密码 */
  savePassword?: boolean
  /** 认证方式（默认 password） */
  authMethod?: 'password' | 'key'
  /** 私钥文件路径（authMethod='key'） */
  privateKeyPath?: string
  /** 密钥口令（不持久化，仅保留在内存） */
  passphrase?: string
  /** 使用本机加密保存的密钥口令 */
  useStoredPassphrase?: boolean
  /** true=保存密钥口令；false=清除；undefined=不动 */
  savePassphrase?: boolean
  /** M1 仅支持 'menu'（堡垒机弹菜单）；'proxyjump' 预留 */
  bastionMode: 'menu' | 'proxyjump'
  /** 掉线自动重连（密码仅存内存，重连时只需重输 MFA） */
  autoReconnect?: boolean
}

export interface AuthPromptPayload {
  connectionId: string
  nonce: string
  name: string
  instructions: string
  prompt: string
  echo: boolean
}

export interface SessionRef {
  sessionId: string
  connectionId: string
}

/** 目录同步（rsync 传输桥）结果 */
export interface SyncResult {
  ok: boolean
  /** 远端 rsync 退出码（0 = 成功） */
  rc: number
  message: string
}

/** SSH 本地端口转发规则（档案级：每个档案独立一份） */
export interface PortForwardRule {
  id: string
  /** 所属档案 id（档案独立） */
  profileId: string
  /** 展示标签 */
  label: string
  /** 本地绑定地址：127.0.0.1 或 0.0.0.0 */
  localHost: string
  localPort: number
  /** 远端目标 host（服务器可达的任意地址） */
  remoteHost: string
  remotePort: number
  /** 连接建立后是否自动启动 */
  enabled: boolean
}

export type PortForwardStatus = 'idle' | 'listening' | 'error'

export interface PortForwardStatusPayload {
  ruleId: string
  status: PortForwardStatus
  message?: string
}

export interface DiagnoseResult {
  ok: boolean
  channelOpened: boolean
  receivedData: boolean
  bytesReceived: number
  message: string
}

export type ConnectionStatus = 'connecting' | 'connected' | 'reconnecting' | 'closed'

export interface ConnectionStatusPayload {
  connectionId: string
  status: ConnectionStatus
  reason?: string
  /** 自动重连时的尝试次数 */
  attempt?: number
}

export interface RestoredPayload {
  connectionId: string
  sessionCount: number
}

export interface SessionDataPayload {
  sessionId: string
  data: Uint8Array
}

export interface SessionClosedPayload {
  sessionId: string
  code?: number
  signal?: string
}

export interface HostKeyPayload {
  connectionId: string
  fingerprint: string
  isNew: boolean
}

export interface QuickCommand {
  id: string
  label: string
  command: string
}

export interface ZmodemEventPayload {
  sessionId: string
  transferId: string
  direction: 'send' | 'receive'
  type: 'start' | 'progress' | 'end' | 'error' | 'info'
  name?: string
  bytesSent?: number
  bytesTotal?: number
  message?: string
}

export type HighlightColor = 'red' | 'yellow' | 'green' | 'blue' | 'orange' | 'purple'

export interface HighlightRule {
  id: string
  /** 正则表达式（JS 语法） */
  pattern: string
  color: HighlightColor
  enabled: boolean
  /** 忽略大小写（默认 true，对英文关键词更贴合日志实践） */
  ignoreCase?: boolean
}

/** 终端配色主题（由 VSCode 主题 JSON 解析而来） */
export interface TerminalTheme {
  name: string
  background: string
  foreground: string
  cursor: string
  selectionBackground: string
  /** ANSI 16 基色：[黑 红 绿 黄 蓝 品红 青 白 | 亮黑 亮红 亮绿 亮黄 亮蓝 亮品红 亮青 亮白] */
  ansi: [
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string
  ]
}

/** 内置默认主题（白天模式，VS Code Light+ 风格终端配色） */
export const DEFAULT_THEME: TerminalTheme = {
  name: 'BastionShell Light（默认）',
  background: '#ffffff',
  foreground: '#333333',
  cursor: '#007acc',
  selectionBackground: 'rgba(0, 122, 204, 0.25)',
  ansi: [
    '#000000',
    '#cd3131',
    '#107c10',
    '#949800',
    '#0451a5',
    '#bc05bc',
    '#0598bc',
    '#555555',
    '#666666',
    '#cd3131',
    '#14ce14',
    '#b5ba00',
    '#0451a5',
    '#bc05bc',
    '#0598bc',
    '#a5a5a5'
  ]
}

/** 内置暗色主题 */
export const DARK_THEME: TerminalTheme = {
  name: 'BastionShell Dark',
  background: '#12121a',
  foreground: '#e4e4ef',
  cursor: '#8ab4ff',
  selectionBackground: 'rgba(90, 120, 255, 0.35)',
  ansi: [
    '#000000',
    '#cd3131',
    '#0dbc79',
    '#e5e510',
    '#2472c8',
    '#bc3fbc',
    '#11a8cd',
    '#e5e5e5',
    '#666666',
    '#f14c4c',
    '#23d18b',
    '#f5f543',
    '#3b8eea',
    '#d670d6',
    '#29b8db',
    '#e5e5e5'
  ]
}

/** 部署任务（持久化到 localStorage，可反复一键执行）
 *  - profileId：堡垒机档案（连接入口，一次 MFA）
 *  - hosts：目标机器 host 列表（过堡垒机菜单批量连接）
 *  - uploads：要上传的本地文件路径列表（rz 上传到远程当前目录，靠前置命令 cd 过去）
 *  - preCommand：上传前执行的命令（如 sudo -i；最后一条命令 cd 到上传目录）
 *  - script：上传完成后执行的脚本
 */
export interface DeployTask {
  id: string
  name: string
  profileId: string
  hosts: string[]
  uploads: string[]
  preCommand: string
  script: string
  /** 堡垒机二级菜单选择用户序号（默认 1） */
  userChoice: string
}

/** preload 暴露给渲染进程的 API 形状 */
export interface BastionApi {
  connect(req: ConnectRequest): Promise<{ connectionId: string }>
  answerAuth(nonce: string, value: string | null): Promise<boolean>
  closeConnection(connectionId: string): Promise<void>
  diagnose(connectionId: string): Promise<DiagnoseResult>
  openSession(opts: { connectionId: string; cols: number; rows: number }): Promise<SessionRef>
  writeSession(sessionId: string, data: string | Uint8Array): void
  resizeSession(sessionId: string, cols: number, rows: number): void
  closeSession(sessionId: string): void
  sendFiles(sessionId: string, paths: string[], overwrite?: 'skip' | 'overwrite' | 'rename'): Promise<void>
  /** 判断本地路径是否为目录（拖拽分流：文件夹→rsync 同步 / 文件→rz） */
  statPath(path: string): Promise<{ isDir: boolean; size: number }>
  /** 目录同步（rsync 传输桥：本地 ↔ 目标机，块级增量） */
  syncDirectory(req: {
    sessionId: string
    localPath: string
    remotePath: string
    direction: 'upload' | 'download'
  }): Promise<SyncResult>
  cancelTransfer(sessionId: string): void
  getPathForFile(file: File): string
  listQuickCommands(): Promise<QuickCommand[]>
  saveQuickCommand(cmd: QuickCommand): Promise<QuickCommand>
  deleteQuickCommand(id: string): Promise<void>
  sendCommand(sessionId: string, command: string): void
  listHighlightRules(): Promise<HighlightRule[]>
  saveHighlightRule(rule: HighlightRule): Promise<HighlightRule>
  deleteHighlightRule(id: string): Promise<void>
  /** 当前终端主题（默认主题则返回 DEFAULT_THEME） */
  getTheme(): Promise<TerminalTheme>
  /** 弹文件选择框导入 VSCode 主题 JSON；取消返回 null */
  importTheme(): Promise<TerminalTheme | null>
  /** 恢复默认主题（白天） */
  resetTheme(): Promise<TerminalTheme>
  /** 切换内置暗色主题 */
  builtinDarkTheme(): Promise<TerminalTheme>
  /** 打开外部链接（仅 http/https 白名单） */
  openExternal(url: string): Promise<boolean>
  /** 渲染层日志上报主进程（落盘诊断） */
  logToMain(level: 'info' | 'error', message: string): void
  /** 读取系统剪贴板文本 */
  readClipboard(): Promise<string>
  /** 写入系统剪贴板（供远端 vim OSC 52 复制桥接使用） */
  writeClipboard(text: string): void
  listProfiles(): Promise<ConnectionProfile[]>
  saveProfile(profile: ConnectionProfile): Promise<ConnectionProfile>
  deleteProfile(id: string): Promise<void>
  /** 弹文件选择框选私钥文件（OpenSSH PEM），取消返回 null */
  pickPrivateKey(): Promise<string | null>
  /** 端口转发：列出某档案的转发规则 */
  listForwardRules(profileId: string): Promise<PortForwardRule[]>
  /** 端口转发：保存（新增/更新）一条规则 */
  saveForwardRule(profileId: string, rule: PortForwardRule): Promise<PortForwardRule>
  /** 端口转发：删除一条规则 */
  deleteForwardRule(ruleId: string): Promise<void>
  /** 端口转发：在指定连接上启动一条规则 */
  startForward(connectionId: string, rule: PortForwardRule): Promise<{ ok: boolean; message: string }>
  /** 端口转发：停止一条规则 */
  stopForward(ruleId: string): Promise<void>
  /** 端口转发运行时状态订阅 */
  onForwardStatus(cb: (p: PortForwardStatusPayload) => void): () => void
  onAuthPrompt(cb: (p: AuthPromptPayload) => void): () => void
  onConnectionStatus(cb: (p: ConnectionStatusPayload) => void): () => void
  onRestored(cb: (p: RestoredPayload) => void): () => void
  onSessionData(cb: (p: SessionDataPayload) => void): () => void
  onSessionClosed(cb: (p: SessionClosedPayload) => void): () => void
  onHostKey(cb: (p: HostKeyPayload) => void): () => void
  onZmodemEvent(cb: (p: ZmodemEventPayload) => void): () => void
  /** 原生文件多选对话框，返回真实本地路径 */
  pickFiles(): Promise<string[]>
}
