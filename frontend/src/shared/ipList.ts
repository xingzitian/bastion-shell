/** 批量连接：IP/主机名列表解析（逗号、中文逗号、分号、空格、换行均可作分隔） */
export function parseIpList(text: string, max = 30): { hosts: string[]; skipped: number } {
  const tokens = text
    .split(/[,，;；\s]+/)
    .map((t) => t.trim())
    .filter((t) => t.length > 0)
  const seen = new Set<string>()
  const hosts: string[] = []
  let skipped = 0
  for (const t of tokens) {
    if (t.length > 253 || seen.has(t)) {
      skipped++
      continue
    }
    seen.add(t)
    if (hosts.length >= max) {
      skipped++
      continue
    }
    hosts.push(t)
  }
  return { hosts, skipped }
}
