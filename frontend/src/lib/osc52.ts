/**
 * OSC 52 剪贴板桥接（远端 vim/终端程序 → 本机剪贴板）。
 * 序列格式：ESC ] 52 ; Pc ; Pd BEL
 *   Pc：剪贴板选择（c=clipboard、p=primary、s=select、q=secondary）
 *   Pd：base64 编码的剪贴板内容（"?" 开头为查询请求，不回写）
 * 参考 xterm 的 OSC 52 规范（https://invisible-island.net/xterm/ctlseqs/ctlseqs.html 的 OSC 52）。
 */

export interface Osc52Payload {
  selection: string
  base64: string
}

/** 解析 OSC 52 数据段（xterm.js 的 registerOscHandler(52) 回调收到的 data 形如 "c;<base64>"） */
export function parseOsc52(data: string): Osc52Payload | null {
  if (!data) return null
  const idx = data.indexOf(';')
  const selection = idx < 0 ? data : data.slice(0, idx)
  const base64 = idx < 0 ? '' : data.slice(idx + 1)
  // 查询请求（? 开头）或空载荷：不产生写剪贴板动作
  if (!base64 || base64.startsWith('?')) return null
  return { selection, base64 }
}

/** base64（OSC 52 载荷）→ UTF-8 文本；非法输入返回 null */
export function decodeOsc52Base64(base64: string): string | null {
  try {
    if (!/^[A-Za-z0-9+/=]*$/.test(base64) || base64.length % 4 === 1) return null
    const bin = atob(base64)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    return new TextDecoder('utf-8', { fatal: false }).decode(bytes)
  } catch {
    return null
  }
}
