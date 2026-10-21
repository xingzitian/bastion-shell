// 桌面版 shim 的行为测试（桌面版前端原来没有任何测试，这批先立个最小的）。
//
// 盯的是维护者实际踩到的那个坑：**没有已存密码时，不要真的把连接请求发出去**。
// 以前会把空密码发下去，SSH 客户端于是"一个认证方式都没有"，最后报一句
// no supported methods remain —— 用户看不懂也不知道该干什么。
//
// 做法：用 vm 造一个假的浏览器环境加载 shim.js（它只是个 IIFE，加载时只挂 window.bastion），
// 然后真的调 window.bastion.connect()，看它**有没有创建 WebSocket**（= 有没有把请求发出去）。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import * as fs from 'node:fs'
import * as path from 'node:path'
import * as vm from 'node:vm'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const shimCode = fs.readFileSync(path.join(here, '..', 'public', 'shim.js'), 'utf8')

function loadShim(savedSecrets) {
  const store = new Map()
  const fetchCalls = []
  const wsCreated = []
  const window = {}

  // 定时器 unref：shim 里有"连接超时"这类计时器，不 unref 的话
  // 测试进程要陪着它们等完（实测拖到 30 秒）。
  const patchedSetTimeout = (fn, ms, ...a) => {
    const h = setTimeout(fn, ms, ...a)
    if (h && typeof h.unref === 'function') h.unref()
    return h
  }

  const sandbox = {
    window,
    console,
    setTimeout: patchedSetTimeout,
    clearTimeout,
    setInterval: patchedSetTimeout,
    clearInterval,
    localStorage: {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => void store.set(k, String(v)),
      removeItem: (k) => void store.delete(k)
    },
    document: {
      createElement: () => ({ style: {}, appendChild() {}, setAttribute() {}, addEventListener() {} }),
      addEventListener() {},
      body: { appendChild() {} }
    },
    navigator: { clipboard: { writeText: async () => {} }, userAgent: 'node' },
    FileReader: class {},
    fetch: async (url) => {
      const u = String(url)
      fetchCalls.push(u)
      const key = decodeURIComponent((u.split('key=')[1] || '').split('&')[0])
      return { ok: true, status: 200, json: async () => ({ value: (savedSecrets || {})[key] || '' }) }
    },
    // 假 WebSocket：**必须真的触发 open**，否则 shim 的 openWS 会一直等到"连接超时"
    WebSocket: class {
      constructor(u) {
        wsCreated.push(String(u))
        this.readyState = 0
        this.handlers = {}
        setTimeout(() => {
          this.readyState = 1
          if (this.onopen) this.onopen({})
          ;(this.handlers.open || []).forEach((cb) => cb({}))
        }, 0)
      }
      addEventListener(t, cb) {
        ;(this.handlers[t] = this.handlers[t] || []).push(cb)
      }
      removeEventListener(t, cb) {
        this.handlers[t] = (this.handlers[t] || []).filter((x) => x !== cb)
      }
      send() {}
      close() {
        this.readyState = 3
      }
    }
  }

  vm.createContext(sandbox)
  vm.runInContext(shimCode, sandbox)
  return { api: window.bastion, fetchCalls, wsCreated }
}

const profile = { id: 'p1', name: 'p1', host: '10.0.0.1', port: 22, username: 'deploy' }

test('没有已存密码：不发连接请求，而是直接说清楚要做什么', async () => {
  const { api, wsCreated } = loadShim({}) // 钥匙串里没有任何密码
  const r = await api.connect({ profile, useStoredPassword: true, authMethod: 'password' })
  assert.equal(r && r.ok, false, '应该直接失败返回，而不是去连')
  assert.match(String(r && r.message), /没有可用的密码|还没有保存密码/, '要说人话：让他输一次密码')
  assert.equal(wsCreated.length, 0, '★ 关键：一个 WebSocket 都不该建 —— 空密码根本不该发出去')
})

test('手输了密码：正常往下走（守门不能把正常连接也拦了）', async () => {
  const { api, wsCreated } = loadShim({})
  void api
    .connect({ profile, useStoredPassword: false, password: 'pw-123', authMethod: 'password' })
    .catch(() => {})
  await new Promise((r) => setTimeout(r, 20))
  assert.equal(wsCreated.length, 1, '有密码时应该真的发起连接')
})

test('钥匙串里有密码：正常往下走', async () => {
  const { api, wsCreated } = loadShim({ p1: 'saved-pw' })
  void api.connect({ profile, useStoredPassword: true, authMethod: 'password' }).catch(() => {})
  await new Promise((r) => setTimeout(r, 20))
  assert.equal(wsCreated.length, 1, '取了钥匙串里的密码之后应该发起连接')
})

test('私钥认证：不因为没有密码就被拦下', async () => {
  const { api, wsCreated } = loadShim({})
  void api
    .connect({
      profile,
      useStoredPassword: false,
      authMethod: 'key',
      privateKeyPath: 'C:/keys/id_ed25519',
      passphrase: ''
    })
    .catch(() => {})
  await new Promise((r) => setTimeout(r, 20))
  assert.equal(wsCreated.length, 1, '走私钥时不看密码，不该被拦')
})
