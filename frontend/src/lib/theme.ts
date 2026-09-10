import type { TerminalTheme } from '../shared/types'
import type { ITheme } from '@xterm/xterm'

/** TerminalTheme → xterm.js ITheme 映射（含 ANSI 16 基色） */
export function toXtermTheme(t: TerminalTheme): ITheme {
  const [black, red, green, yellow, blue, magenta, cyan, white, brightBlack, brightRed, brightGreen, brightYellow, brightBlue, brightMagenta, brightCyan, brightWhite] =
    t.ansi
  return {
    background: t.background,
    foreground: t.foreground,
    cursor: t.cursor,
    selectionBackground: t.selectionBackground,
    black,
    red,
    green,
    yellow,
    blue,
    magenta,
    cyan,
    white,
    brightBlack,
    brightRed,
    brightGreen,
    brightYellow,
    brightBlue,
    brightMagenta,
    brightCyan,
    brightWhite
  }
}
