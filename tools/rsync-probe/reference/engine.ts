import { createServer, Server, Socket } from 'net'
import { spawn, ChildProcess } from 'child_process'
import { mkdirSync, writeFileSync } from 'fs'
import { join } from 'path'
import { logger } from '../logger'

/**
 * 基于 rsync 的"目录同步"传输桥（插件式模块，可选启用）。
 *
 * 原理（已在 WSL 内用 spike 完整验证）：
 *   本地 rsync -a -e <桥> src host:dst  ←→  本地 TCP socket 桥  ←→
 *   raw pty 上的远端 `rsync --server`（经堡垒机菜单→目标机 shell 注入）。
 *
 * 关键技术点（详见 scripts/bridge_server.py 的 spike 记录）：
 *   1. rsync 3.2+ 握手是二进制 write_int(协议号)，不是 @RSYNCD: 文本；
 *   2. 选项串里的 'v' 会触发 do_negotiated_strings（桥接场景死锁）→ 注入时剥掉；
 *   3. 完成标记 __RSYNC_DONE_<rc>__ 判定接收端退出码（发送端等第二次 NDX_DONE 是
 *      sender/generator 多进程细节，不影响正确性）→ 干净退出 + 会话保留。
 */

const MARK = Buffer.from('__RSYNC_DONE_')
// rsync 二进制握手 = [协议号 1 字节(29~33)][0x00 0x00 0x00]，远端 3.1.x 是 31、3.2+ 是 32
const GREETING_NULS = Buffer.from([0x00, 0x00, 0x00])

/** 找到握手起始位置（协议号字节的下标）；找不到返回 -1 */
function findGreetingStart(buf: Buffer): number {
  const idx = buf.indexOf(GREETING_NULS)
  if (idx <= 0) return -1
  return idx - 1
}

export interface SyncConfig {
  /** Windows 原生 rsync.exe 绝对路径 */
  rsyncExe: string
  /** Windows 侧 node.exe 绝对路径（桥客户端用它运行） */
  nodeExe: string
  /** 桥客户端脚本落盘目录（Windows 路径，运行时写入） */
  bridgeDir: string
}

export interface SyncRequest {
  localPath: string
  remotePath: string
  direction: 'upload' | 'download'
}

export interface SyncResult {
  ok: boolean
  rc: number
  message: string
}

/** 桥客户端（Node 单文件）：连接本机 socket → 发远端命令 → 桥 stdio↔socket */
const BRIDGE_CLIENT_SOURCE = `const net = require('net');
const port = parseInt(process.argv[2], 10);
const cmd = process.argv.slice(4).join(' ');
const sock = net.connect(port, '127.0.0.1');
sock.on('connect', function () {
  sock.write(cmd + '\\n');
  process.stdin.pipe(sock);
  sock.pipe(process.stdout);
});
sock.on('error', function () { process.exit(1); });
sock.on('close', function () { process.exit(0); });
`

/** Windows 路径 → WSL 路径（C:\\x\\y → /mnt/c/x/y） */
export function windowsToWsl(p: string): string {
  const m = p.match(/^([A-Za-z]):(.*)$/)
  if (!m) return p.replace(/\\/g, '/')
  return '/mnt/' + m[1].toLowerCase() + m[2].replace(/\\/g, '/')
}

/** 剥离 --server 选项串里的 'v'（触发 negotiated strings 死锁），版本无关（3.1~3.5 都适用） */
export function stripNegotiatedV(remoteCmd: string): string {
  return remoteCmd.replace(/(--server\s+-[^ ]*)v([^ ]*)/, '$1$2')
}

/** 单会话同步引擎（一次一个同步） */
export class SyncEngine {
  private server: Server | null = null
  private sock: Socket | null = null
  private child: ChildProcess | null = null
  private active = false
  private sessionWrite: (data: Buffer) => void

  // 桥接状态
  private greetingSeen = false
  private cmdRead = false
  private clientPending: Buffer = Buffer.alloc(0)
  private greetBuf: Buffer = Buffer.alloc(0)
  private markBuf: Buffer = Buffer.alloc(0)
  private resolveDone: ((r: SyncResult) => void) | null = null
  private rejectDone: ((e: Error) => void) | null = null
  /** 完成标记已出现：停止向会话转发客户端数据（防脏字节污染 shell） */
  private done = false

  constructor(private config: SyncConfig, write: (data: Buffer) => void) {
    this.sessionWrite = write
  }

  get isActive(): boolean {
    return this.active
  }

  /** 会话数据 → 桥 socket（握手 drain + 完成标记检测） */
  consumeFromSession(data: Buffer): void {
    if (!this.sock) return
    if (!this.greetingSeen) {
      this.greetBuf = Buffer.concat([this.greetBuf, data])
      const i = findGreetingStart(this.greetBuf)
      if (i >= 0) {
        this.greetingSeen = true
        this.sock.write(this.greetBuf.subarray(i))
        // 远端已确认 raw（发来握手），把客户端积压的握手字节灌进会话
        this.flushClientPending()
      }
      return
    }
    this.markBuf = Buffer.concat([this.markBuf, data])
    const mi = this.markBuf.indexOf(MARK)
    if (mi >= 0) {
      // 完成：停止把客户端多余字节转给会话，并恢复终端（Ctrl+C 弹回被脏输入卡住的 readline）
      this.done = true
      const rest = this.markBuf.subarray(mi + MARK.length)
      const end = rest.indexOf(Buffer.from('__'))
      let rc = 0
      if (end >= 0) {
        const s = rest.subarray(0, end).toString()
        const n = parseInt(s, 10)
        if (Number.isFinite(n)) rc = n
      }
      // 转发 MARK 之前的数据（服务端最后的状态字节）
      try {
        this.sock.write(this.markBuf.subarray(0, mi))
      } catch {
        /* ignore */
      }
      const friendly =
        rc === 0 ? '同步完成' : rc === 127 ? '远端未安装 rsync' : `同步失败（远端退出码 ${rc}）`
      // 恢复终端：Ctrl+C 取消可能被脏输入卡住的 readline（isig 生效时等效 SIGINT）
      try {
        this.sessionWrite(Buffer.from([0x03]))
      } catch {
        /* ignore */
      }
      this.resolveDone?.({ ok: rc === 0, rc, message: friendly })
      this.resolveDone = null
      this.rejectDone = null
      return
    }
    // 只保留"可能是 MARK 前缀"的后缀，其余立即转发（避免扣住小数据）
    let keep = 0
    for (let k = 1; k < MARK.length; k++) {
      if (this.markBuf.length >= k && this.markBuf.subarray(this.markBuf.length - k).equals(MARK.subarray(0, k))) {
        keep = k
      }
    }
    const forward = this.markBuf.subarray(0, this.markBuf.length - keep)
    if (forward.length > 0) this.sock.write(forward)
    this.markBuf = this.markBuf.subarray(this.markBuf.length - keep)
  }

  async sync(req: SyncRequest): Promise<SyncResult> {
    if (this.active) throw new Error('已有同步在进行')
    this.active = true
    let childStderr = ''
    try {
      // 1) 写桥客户端脚本
      const bridgeClient = join(this.config.bridgeDir, 'sync_bridge_client.js')
      mkdirSync(this.config.bridgeDir, { recursive: true })
      writeFileSync(bridgeClient, BRIDGE_CLIENT_SOURCE, 'utf8')

      // 2) 起本地 TCP 服务（127.0.0.1:0）
      const port = await new Promise<number>((resolve, reject) => {
        const srv = createServer()
        srv.on('error', reject)
        srv.listen(0, '127.0.0.1', () => {
          const a = srv.address() as { port: number }
          this.server = srv
          resolve(a.port)
        })
      })

      // 3) 起本地 rsync（Windows 原生，直接 spawn，无 WSL/路径转换）
      const localPath = req.localPath.replace(/\\/g, '/')
      const remote = req.remotePath
      const src = req.direction === 'upload' ? localPath : `host:${remote}/`
      const dst = req.direction === 'upload' ? `host:${remote}/` : localPath
      // 桥命令用正斜杠（sh/cmd 都能正确解析空格路径，反斜杠在 sh 里会被当转义）
      const rsh = `"${this.config.nodeExe.replace(/\\/g, '/')}" "${bridgeClient.replace(/\\/g, '/')}" ${port}`
      this.child = spawn(this.config.rsyncExe, ['-a', '-e', rsh, src, dst], {
        stdio: ['ignore', 'ignore', 'pipe']
      })
      logger.info(`[sync] 本地 rsync 已启动: ${this.config.rsyncExe} -a -e ... ${src} ${dst}`)
      this.child.stderr?.on('data', (d: Buffer) => {
        childStderr += d.toString('utf8')
      })
      this.child.on('exit', (code) => {
        logger.info(`[sync] 本地 rsync 退出 code=${code} stderr=${childStderr.trim().slice(0, 200)}`)
      })

      // 4) 等桥连接 + 读远端命令
      const remoteCmd = await new Promise<string>((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('等待 rsync 桥连接超时')), 15000)
        if (!this.server) {
          clearTimeout(timer)
          reject(new Error('TCP 服务未就绪'))
          return
        }
        this.server.once('connection', (sock: Socket) => {
          this.sock = sock
          logger.info('[sync] 桥客户端已连接')
          sock.on('data', (d: Buffer) => this.handleSocketData(d))
          sock.on('close', () => this.handleSocketClose())
          sock.on('error', () => this.handleSocketClose())
        })
        // 上面的 connection 回调里要拿到 cmd 再 resolve；用一个共享状态
        this.resolveCmd = resolve
        this.rejectCmd = reject
        this.cmdTimer = timer
      })

      // 5) 剥离 'v' + 注入远端命令（timeout 兜底：rsync 挂死也会被超时杀掉 → stty sane 恢复终端）
      const forced = stripNegotiatedV(remoteCmd)
      logger.info(`[sync] 注入远端命令: ${forced}`)
      this.sessionWrite(
        Buffer.from(
          `stty raw -echo -iexten 2>/dev/null; PS1=; timeout 120 ${forced} 2>/tmp/rsync_sync_err.txt; rc=$?; stty sane 2>/dev/null; echo __RSYNC_DONE_\${rc}__\r`,
          'utf8'
        )
      )

      // 6) 等待完成（DONE 标记 / 客户端退出 / 超时）
      const result = await new Promise<SyncResult>((resolve, reject) => {
        this.resolveDone = resolve
        this.rejectDone = reject
        const t = setTimeout(() => {
          this.resolveDone = null
          this.rejectDone = null
          if (this.sock) {
            this.sock.end()
          }
          resolve({
            ok: false,
            rc: -1,
            message: `同步超时${childStderr ? `（rsync: ${childStderr.trim().slice(0, 200)}）` : ''}`
          })
        }, 130000)
        this.doneTimer = t
      })
      logger.info(`[sync] 结果 rc=${result.rc} ok=${result.ok} msg=${result.message}`)
      return result
    } finally {
      this.cleanup()
      this.active = false
    }
  }

  // ---- 内部 ----
  private resolveCmd: ((cmd: string) => void) | null = null
  private rejectCmd: ((e: Error) => void) | null = null
  private cmdTimer: NodeJS.Timeout | null = null
  private doneTimer: NodeJS.Timeout | null = null

  private handleSocketData(d: Buffer): void {
    // 完成标记已出现：丢弃客户端在完成后的多余字节（防污染 shell 输入）
    if (this.done) return
    if (!this.cmdRead) {
      if (this.cmdBuf) {
        d = Buffer.concat([this.cmdBuf, d])
        this.cmdBuf = null
      }
      const nl = d.indexOf(0x0a)
      if (nl < 0) {
        this.cmdBuf = d
        return
      }
      this.cmdRead = true
      const cmd = d.subarray(0, nl).toString('utf8')
      const leftover = d.subarray(nl + 1)
      if (this.cmdTimer) clearTimeout(this.cmdTimer)
      this.resolveCmd?.(cmd)
      this.resolveCmd = null
      this.clientPending = leftover
      if (this.greetingSeen) this.flushClientPending()
      return
    }
    if (this.greetingSeen) {
      this.sessionWrite(d)
    } else {
      this.clientPending = Buffer.concat([this.clientPending, d])
    }
  }

  private cmdBuf: Buffer | null = null

  private handleSocketClose(): void {
    // 客户端提前断开：终止
    if (this.resolveDone) {
      this.resolveDone({ ok: false, rc: -1, message: '同步连接中断' })
      this.resolveDone = null
      this.rejectDone = null
    }
  }

  /** 远端握手出现后，把客户端积压的字节灌进会话 */
  private flushClientPending(): void {
    if (this.clientPending.length > 0 && this.greetingSeen) {
      this.sessionWrite(this.clientPending)
      this.clientPending = Buffer.alloc(0)
    }
  }

  private cleanup(): void {
    if (this.doneTimer) clearTimeout(this.doneTimer)
    if (this.cmdTimer) clearTimeout(this.cmdTimer)
    try {
      this.sock?.end()
    } catch {
      /* ignore */
    }
    try {
      this.server?.close()
    } catch {
      /* ignore */
    }
    if (this.child && this.child.exitCode === null) {
      try {
        this.child.kill()
      } catch {
        /* ignore */
      }
    }
    this.server = null
    this.sock = null
    this.child = null
    this.greetingSeen = false
    this.cmdRead = false
    this.done = false
    this.clientPending = Buffer.alloc(0)
    this.greetBuf = Buffer.alloc(0)
    this.markBuf = Buffer.alloc(0)
    this.cmdBuf = null
    this.resolveDone = null
    this.rejectDone = null
    this.resolveCmd = null
    this.rejectCmd = null
  }
}
