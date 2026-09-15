package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 官方 MCP SDK 互操作检查（实机 + 真 HTTP + 真会话 + 真文件传输）。
//
// 为什么值得单独做：自己写的 JSON-RPC 测试只能证明「我认为协议是这样」。
// 而 VS Code / Copilot / Claude 这些客户端用的是 `@modelcontextprotocol/sdk` ——
// 让它去连**真在跑的 Go 端点**、跑**真命令**、传**真文件**，才能证明
// 「AI 真的能操作桌面版」。
//
// 需要 `BASTION_E2E_MCP_SDK_DIR` 指向一个装了 sdk 的目录（就是扩展侧
// %TEMP%\mcp-client-check 那个），没设置就跳过。
//
// 工具数和扩展侧不同（桌面版 8 个、扩展 9 个：少了 bastion_connect），
// 所以断言清单是各写一份的。

const desktopInteropScript = `// 自动生成，别手改 —— 见 bastion-wails/mcp_interop_e2e_test.go
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js'

const [url, token, localFile, remoteDir, localDir] = process.argv.slice(2)
const fails = []
const check = (name, ok, extra = '') => {
  console.log((ok ? '✔ ' : '✖ ') + name + (extra ? ' — ' + extra : ''))
  if (!ok) fails.push(name)
}
const text = (r) => (r?.content?.[0]?.text ?? '')

const client = new Client({ name: 'bastion-desktop-interop', version: '1.0.0' })
await client.connect(
  new StreamableHTTPClientTransport(new URL(url), { requestInit: { headers: { Authorization: 'Bearer ' + token } } })
)
const info = client.getServerVersion()
check('握手成功（initialize + notifications/initialized）', true, (info?.name ?? '?') + '@' + (info?.version ?? '?'))

const { tools } = await client.listTools()
check('tools/list 返回 8 个工具', tools.length === 8, tools.map((t) => t.name).join(', '))
const want = ['bastion_listSessions', 'bastion_health', 'bastion_tail', 'bastion_listProfiles', 'bastion_exec', 'bastion_habits', 'bastion_push', 'bastion_pull']
check('工具名与扩展/文档一致', want.every((n) => tools.some((t) => t.name === n)), want.join(', '))
check('只读标注生效（客户端据此不弹确认框）', tools.find((t) => t.name === 'bastion_listSessions')?.annotations?.readOnlyHint === true)
check(
  '有副作用的工具不是只读',
  ['bastion_exec', 'bastion_habits', 'bastion_push', 'bastion_pull'].every(
    (n) => tools.find((t) => t.name === n)?.annotations?.readOnlyHint !== true
  )
)
check('inputSchema 齐全', tools.every((t) => t.inputSchema && t.inputSchema.type === 'object'))

const s = await client.callTool({ name: 'bastion_listSessions', arguments: {} })
check('列会话可用', s.isError !== true && /会话/.test(text(s)))

const h = await client.callTool({ name: 'bastion_health', arguments: {} })
check('自检可用（AI 出错后的第一条退路）', h.isError !== true && /端点自检/.test(text(h)))
check('自检里报得出共享规则目录', /共享规则目录/.test(text(h)))

const t = await client.callTool({ name: 'bastion_tail', arguments: { lines: 5 } })
check('读屏幕可用', t.isError !== true && /屏幕最后 5 行/.test(text(t)))

const e = await client.callTool({ name: 'bastion_exec', arguments: { command: 'echo sdk-interop-ok' } })
check('exec 真的在远端跑了命令', /sdk-interop-ok/.test(text(e)), JSON.stringify(text(e)).slice(0, 80))
check('exec 带回了退出码', /退出码 0/.test(text(e)))

const bad = await client.callTool({ name: 'bastion_exec', arguments: {} })
check('缺参数的调用得到 isError 与可读原因', bad.isError === true && /需要 command/.test(text(bad)))

const danger = await client.callTool({ name: 'bastion_exec', arguments: { command: 'rm -rf /' } })
check('高危命令被拦下（没发出去）', /高危规则拦下/.test(text(danger)) && /没有发出去/.test(text(danger)))

const badSession = await client.callTool({ name: 'bastion_tail', arguments: { terminal: '根本没有这条会话' } })
check('会话名写错时给出可操作的提示', /找不到会话/.test(text(badSession)))

const hb = await client.callTool({ name: 'bastion_habits', arguments: { action: 'read' } })
check('个人习惯可读（共享文件）', /提权习惯/.test(text(hb)))

const push = await client.callTool({ name: 'bastion_push', arguments: { localPath: localFile, remoteDir: remoteDir } })
check('push 真的把文件传上去了', /✅ 已上传/.test(text(push)), JSON.stringify(text(push)).slice(0, 140))
check('push 带回了回读证据', /判定依据（传完回读远端）/.test(text(push)) && /不要再用 ls \/ md5sum/.test(text(push)))

const pull = await client.callTool({ name: 'bastion_pull', arguments: { remotePath: remoteDir + '/sdk-push.txt', localDir: localDir } })
check('pull 真的把文件拉回来了', /✅ 已下载/.test(text(pull)), JSON.stringify(text(pull)).slice(0, 140))

let unknownRejected = false
try {
  await client.callTool({ name: 'bastion_connect', arguments: { profile: 'x' } })
} catch {
  unknownRejected = true // 服务端回 -32602，SDK 会抛
}
check('桌面版没有的工具被明确拒绝（不是假装有）', unknownRejected)

let rejected = false
try {
  const badClient = new Client({ name: 'bad', version: '1.0.0' })
  await badClient.connect(
    new StreamableHTTPClientTransport(new URL(url), { requestInit: { headers: { Authorization: 'Bearer wrong-token' } } })
  )
} catch {
  rejected = true
}
check('错误 token 的客户端连不上（401）', rejected)

await client.close()
console.log(fails.length === 0 ? '\n全部通过 ✅' : '\n失败 ' + fails.length + ' 项 ❌: ' + fails.join(', '))
process.exitCode = fails.length === 0 ? 0 : 1
`

func TestE2EOfficialMcpSdkInterop(t *testing.T) {
	sdkDir := strings.TrimSpace(os.Getenv("BASTION_E2E_MCP_SDK_DIR"))
	if sdkDir == "" {
		t.Skip("未设置 BASTION_E2E_MCP_SDK_DIR，跳过官方 SDK 互操作检查")
	}
	if _, err := os.Stat(filepath.Join(sdkDir, "node_modules", "@modelcontextprotocol", "sdk")); err != nil {
		t.Skipf("%s 里没有 @modelcontextprotocol/sdk", sdkDir)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("找不到 node")
	}

	// 真会话：SDK 发过来的 bastion_exec / push / pull 会真的在远端跑
	s, _ := e2eSession(t)
	t.Logf("互操作检查用的会话：%s", s.name())

	// 真文件 + 真远端目录：让 push/pull 走完整链路
	remoteDir := e2eRemoteTmp(t, s)
	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "sdk-push.txt")
	if err := os.WriteFile(localFile, []byte("通过官方 SDK 传上来的\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	handle, err := startMcpHTTP(newMcpServer(desktopSessionAPI{}), newMcpToken(), "127.0.0.1", 0, nil)
	if err != nil {
		t.Fatalf("起 MCP 端点失败：%v", err)
	}
	defer handle.Close()

	script := filepath.Join(sdkDir, "check-desktop.mjs")
	if err := os.WriteFile(script, []byte(desktopInteropScript), 0o644); err != nil {
		t.Fatalf("写检查脚本失败：%v", err)
	}
	cmd := exec.Command(node, script, handle.url, handle.token, localFile, remoteDir, localDir)
	cmd.Dir = sdkDir
	out, err := cmd.CombinedOutput()
	t.Logf("官方 SDK 检查输出：\n%s", out)
	if err != nil {
		t.Fatalf("官方 SDK 互操作检查未通过：%v", err)
	}
	if !strings.Contains(string(out), "全部通过") {
		t.Fatal("官方 SDK 检查没有报「全部通过」")
	}
}
