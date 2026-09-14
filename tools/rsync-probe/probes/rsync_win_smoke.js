const net = require('net')
const { spawn } = require('child_process')
const fs = require('fs')
const path = require('path')
const os = require('os')

const RSYNC = process.env.LOCALAPPDATA + '\\rsync\\rsync.exe'
const NODE = 'C:\\Program Files\\nodejs\\node.exe'

// 写桥客户端
const bridgeDir = path.join(os.tmpdir(), 'sync-test')
fs.mkdirSync(bridgeDir, { recursive: true })
const bridgePath = path.join(bridgeDir, 'bridge.js')
fs.writeFileSync(
  bridgePath,
  `const net=require('net');const port=parseInt(process.argv[2],10);const cmd=process.argv.slice(4).join(' ');const s=net.connect(port,'127.0.0.1');s.on('connect',function(){s.write(cmd+'\\n');process.stdin.pipe(s);s.pipe(process.stdout)});s.on('error',function(){process.exit(1)});s.on('close',function(){process.exit(0)});`
)

const src = path.join(os.tmpdir(), 'sync-src')
const dst = path.join(os.tmpdir(), 'sync-dst')
fs.rmSync(src, { recursive: true, force: true })
fs.rmSync(dst, { recursive: true, force: true })
fs.mkdirSync(src, { recursive: true })
fs.writeFileSync(path.join(src, 'hello.txt'), 'hello rsync windows\n')

const srv = net.createServer((sock) => {
  sock.on('data', (d) => {
    const s = d.toString()
    if (s.includes('\n')) {
      console.log('BRIDGE_CMD=', s.split('\n')[0])
      setTimeout(() => {
        sock.destroy()
        srv.close()
      }, 800)
    }
  })
})

srv.listen(0, '127.0.0.1', () => {
  const port = srv.address().port
  const rsh = `"${NODE.replace(/\\/g, '/')}" "${bridgePath.replace(/\\/g, '/')}" ${port}`
  console.log('SPAWNING rsync with -e bridge...')
  const child = spawn(RSYNC, ['-a', '-e', rsh, src, 'host:' + dst + '/'], { stdio: ['ignore', 'ignore', 'pipe'] })
  child.stderr.on('data', (d) => process.stderr.write(d))
  child.on('exit', (code) => {
    console.log('RSYNC_EXIT=', code)
    process.exit(0)
  })
  setTimeout(() => {
    console.log('TIMEOUT')
    child.kill()
    process.exit(1)
  }, 15000)
})
