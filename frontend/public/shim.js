/*
 * window.bastion shim：把 Electron 版 React 前端对 IPC 的调用翻译到 Go 后端的
 * WebSocket / HTTP。数据类（档案/快捷命令/高亮/主题/转发规则）存 localStorage，
 * 连接/会话走 /ws。
 */
(function () {
  'use strict';

  var PREFIX = 'bastion-shell-go:';
  function lsGet(key, fallback) {
    try { var v = localStorage.getItem(PREFIX + key); return v === null ? fallback : JSON.parse(v); }
    catch (e) { return fallback; }
  }
  function lsSet(key, val) {
    try { localStorage.setItem(PREFIX + key, JSON.stringify(val)); } catch (e) {}
  }

  // ---- 事件订阅 ----
  var evs = {
    authPrompt: [], connectionStatus: [], restored: [], sessionData: [],
    sessionClosed: [], hostKey: [], zmodemEvent: [], forwardStatus: []
  };
  function emit(name, p) { (evs[name] || []).slice().forEach(function (cb) { try { cb(p); } catch (e) {} }); }
  function on(name, cb) {
    evs[name].push(cb);
    return function () { var i = evs[name].indexOf(cb); if (i >= 0) evs[name].splice(i, 1); };
  }

  var seq = 0;
  function nid(p) { return p + '-' + Date.now().toString(36) + '-' + (++seq); }
  var API_BASE = 'http://127.0.0.1:18090';
  function wsURL() { return API_BASE.replace('http', 'ws') + '/ws'; }
  function toBytes(s) { return new TextEncoder().encode(s); }
  function toStr(d) { return typeof d === 'string' ? d : new TextDecoder().decode(d); }

  // ---- WS 管理 ----
  var controlWS = {};   // connectionId -> WebSocket（持有 SSH 连接）
  var sessionWS = {};   // sessionId -> WebSocket（一个 ssh.Session）
  var pendingUpload = {}; // sessionId -> {resolve, reject}
  var fwOwner = {};     // ruleId -> connectionId
  var pendingFw = {};   // ruleId -> {resolve}
  var fileStore = {};   // token -> File（浏览器拿不到路径，用 token 中转）
  var authConnByNonce = {}; // nonce -> connectionId（MFA 应答路由）

  function openWS(msg, opts) {
    return new Promise(function (resolve, reject) {
      var ws = new WebSocket(wsURL());
      if (opts && opts.onWs) opts.onWs(ws);
      var done = false;
      var timer = setTimeout(function () {
        if (!done) { done = true; reject(new Error('连接超时')); try { ws.close(); } catch (e) {} }
      }, 30000);
      ws.onopen = function () { ws.send(JSON.stringify(msg)); };
      ws.onmessage = function (e) {
        var m;
        try { m = JSON.parse(e.data); } catch (err) { return; }
        if (m.type === 'ready') { clearTimeout(timer); done = true; resolve(ws); return; }
        if (m.type === 'error') {
          clearTimeout(timer); done = true; reject(new Error(m.message || '连接失败'));
          try { ws.close(); } catch (err) {}
          return;
        }
        if (opts && opts.onMessage) opts.onMessage(m, ws);
      };
      ws.onerror = function () {
        if (!done) { clearTimeout(timer); done = true; reject(new Error('WebSocket 错误')); }
      };
      ws.onclose = function () { clearTimeout(timer); if (opts && opts.onClose) opts.onClose(); };
    });
  }

  // ---- 密码加密存取（Go 后端 DPAPI）----
  function getPassword(key) {
    return fetch(API_BASE + '/api/secret?key=' + encodeURIComponent(key))
      .then(function (r) { if (r.status === 404) return null; return r.json(); })
      .then(function (res) { return (res && res.value) || ''; })
      .catch(function () { return ''; });
  }
  function setPassword(key, value) {
    return fetch(API_BASE + '/api/secret', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key: key, value: value })
    }).catch(function () {});
  }
  function deletePassword(key) {
    return fetch(API_BASE + '/api/secret?key=' + encodeURIComponent(key), { method: 'DELETE' }).catch(function () {});
  }

  // ---- 连接 ----
  async function connect(req) {
    var profile = req.profile || {};
    var connId = nid('conn');
    var passwords = lsGet('passwords', {}); // 现在存 {id: true} 标志，真实密码走后端 DPAPI
    var pw = '';
    if (req.useStoredPassword) {
      pw = await getPassword(profile.id);
      // 旧版本明文密码迁移：后端没有、本地有明文字符串则加密入库
      if (!pw && typeof passwords[profile.id] === 'string' && passwords[profile.id]) {
        pw = passwords[profile.id];
        await setPassword(profile.id, pw);
      }
    } else {
      pw = req.password || '';
    }
    if (req.savePassword && pw) {
      await setPassword(profile.id, pw);
    }
    passwords[profile.id] = !!pw;
    lsSet('passwords', passwords);

    var msg = {
      type: 'control',
      connId: connId,
      host: profile.host,
      port: profile.port || 22,
      user: profile.username,
      password: pw,
      key: req.authMethod === 'key' ? (req.privateKeyPath || '') : '',
      passphrase: req.passphrase || ''
    };
    emit('connectionStatus', { connectionId: connId, status: 'connecting' });
    return openWS(msg, {
      onWs: function (ws) { controlWS[connId] = ws; },
      onMessage: function (m) {
        if (m.type === 'auth-prompt') {
          authConnByNonce[m.nonce] = connId;
          emit('authPrompt', {
            connectionId: m.connId, nonce: m.nonce, name: m.name,
            instructions: m.instructions, prompt: m.prompt, echo: m.echo
          });
          return;
        }
        if (m.type === 'forward:started' && pendingFw[m.id]) {
          var p = pendingFw[m.id]; delete pendingFw[m.id];
          emit('forwardStatus', { ruleId: m.id, status: 'listening' });
          p.resolve({ ok: true, message: '' });
        } else if (m.type === 'forward:error' && pendingFw[m.id]) {
          var q = pendingFw[m.id]; delete pendingFw[m.id];
          emit('forwardStatus', { ruleId: m.id, status: 'error', message: m.message });
          q.resolve({ ok: false, message: m.message });
        } else if (m.type === 'forward:stopped') {
          emit('forwardStatus', { ruleId: m.id, status: 'idle' });
        }
      },
      onClose: function () {
        delete controlWS[connId];
        emit('connectionStatus', { connectionId: connId, status: 'closed' });
      }
    }).then(function (ws) {
      controlWS[connId] = ws;
      emit('connectionStatus', { connectionId: connId, status: 'connected' });
      return { connectionId: connId };
    });
  }

  function closeConnection(connectionId) {
    var ws = controlWS[connectionId];
    if (ws) { try { ws.close(); } catch (e) {} }
    return Promise.resolve();
  }

  function diagnose() {
    return Promise.resolve({ ok: true, channelOpened: true, receivedData: true, bytesReceived: 0, message: 'ok' });
  }

  // ---- 会话 ----
  function openSession(opts) {
    var connId = opts.connectionId;
    var sessionId = nid('s');
    var msg = { type: 'connect', connId: connId, cols: opts.cols || 100, rows: opts.rows || 30 };
    return openWS(msg, {
      onMessage: function (m) {
        if (m.type === 'data') {
          emit('sessionData', { sessionId: sessionId, data: toBytes(m.data) });
        } else if (m.type === 'upload:done') {
          var p = pendingUpload[sessionId];
          if (p) { delete pendingUpload[sessionId]; p.resolve(); emit('zmodemEvent', { sessionId: sessionId, transferId: p.transferId, direction: 'send', type: 'end' }); }
        } else if (m.type === 'upload:error') {
          var q = pendingUpload[sessionId];
          if (q) { delete pendingUpload[sessionId]; q.reject(new Error(m.message)); emit('zmodemEvent', { sessionId: sessionId, transferId: q.transferId, direction: 'send', type: 'error', message: m.message }); }
        }
      },
      onClose: function () {
        delete sessionWS[sessionId];
        delete pendingUpload[sessionId];
        emit('sessionClosed', { sessionId: sessionId });
      }
    }).then(function (ws) {
      sessionWS[sessionId] = ws;
      return { sessionId: sessionId, connectionId: connId };
    });
  }

  function writeSession(sessionId, data) {
    var ws = sessionWS[sessionId];
    if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: 'input', data: toStr(data) }));
  }
  function resizeSession(sessionId, cols, rows) {
    var ws = sessionWS[sessionId];
    if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols: cols, rows: rows }));
  }
  function closeSession(sessionId) {
    var ws = sessionWS[sessionId];
    if (ws) { try { ws.close(); } catch (e) {} }
  }
  function sendCommand(sessionId, command) {
    writeSession(sessionId, command + '\r');
  }

  function uploadFileBytes(file) {
    // File → base64 → POST /api/upload → 返回后端临时路径
    return new Promise(function (resolve, reject) {
      var reader = new FileReader();
      reader.onload = function () {
        var b64 = String(reader.result).split(',')[1] || '';
        fetch(API_BASE + '/api/upload', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: file.name, data: b64 })
        }).then(function (r) { return r.json(); })
          .then(function (res) { if (res && res.path) resolve(res.path); else reject(new Error('上传预处理失败')); })
          .catch(reject);
      };
      reader.onerror = function () { reject(new Error('读文件失败')); };
      reader.readAsDataURL(file);
    });
  }

  function sendFiles(sessionId, paths, overwrite) {
    var ws = sessionWS[sessionId];
    if (!ws || ws.readyState !== 1) return Promise.reject(new Error('会话未就绪'));
    paths = paths || [];
    // token → 真实临时路径（拖拽进来的文件走内容上传）
    return Promise.all(paths.map(function (p) {
      var f = fileStore[p];
      if (f) { delete fileStore[p]; return uploadFileBytes(f); }
      return Promise.resolve(p);
    })).then(function (realPaths) {
      var name = realPaths.join(', ');
      return new Promise(function (resolve, reject) {
        var transferId = nid('t');
        pendingUpload[sessionId] = { resolve: resolve, reject: reject, transferId: transferId };
        emit('zmodemEvent', { sessionId: sessionId, transferId: transferId, direction: 'send', type: 'start', name: name });
        ws.send(JSON.stringify({ type: 'upload', paths: realPaths }));
      });
    });
  }

  // ---- 数据存储（localStorage）----

  // 档案
  function listProfiles() {
    var profs = lsGet('profiles', []);
    var passwords = lsGet('passwords', {});
    return Promise.resolve(profs.map(function (p) {
      return Object.assign({}, p, { hasStoredPassword: !!passwords[p.id] });
    }));
  }
  function saveProfile(profile) {
    var profs = lsGet('profiles', []);
    var i = profs.findIndex(function (p) { return p.id === profile.id; });
    if (i >= 0) profs[i] = profile; else profs.push(profile);
    lsSet('profiles', profs);
    return Promise.resolve(profile);
  }
  function deleteProfile(id) {
    lsSet('profiles', lsGet('profiles', []).filter(function (p) { return p.id !== id; }));
    var passwords = lsGet('passwords', {}); delete passwords[id]; lsSet('passwords', passwords);
    deletePassword(id);
    return Promise.resolve();
  }

  // 快捷命令
  function listQuickCommands() { return Promise.resolve(lsGet('quickcommands', [])); }
  function saveQuickCommand(cmd) {
    var list = lsGet('quickcommands', []);
    var i = list.findIndex(function (x) { return x.id === cmd.id; });
    if (i >= 0) list[i] = cmd; else list.push(cmd);
    lsSet('quickcommands', list);
    return Promise.resolve(cmd);
  }
  function deleteQuickCommand(id) {
    lsSet('quickcommands', lsGet('quickcommands', []).filter(function (x) { return x.id !== id; }));
    return Promise.resolve();
  }

  // 高亮规则（默认与 Electron 版一致）
  var DEFAULT_HL = [
    { id: 'default-error', pattern: '\\b(error|errors|failed|failure|fatal|exception|panic)\\b', color: 'red', enabled: true },
    { id: 'default-error-cn', pattern: '错误|失败|异常|无法连接|连接超时', color: 'red', enabled: true },
    { id: 'default-warn', pattern: '\\b(warn|warning|deprecated)\\b', color: 'yellow', enabled: true },
    { id: 'default-warn-cn', pattern: '警告', color: 'yellow', enabled: true },
    { id: 'default-success-cn', pattern: '成功', color: 'green', enabled: true },
    { id: 'default-info', pattern: '\\b(info|information|notice)\\b', color: 'purple', enabled: true },
    { id: 'default-perm', pattern: '\\b(permission denied|access denied|connection refused|timed out|timeout|not found|no such file|connection reset)\\b', color: 'orange', enabled: true },
    { id: 'default-ip', pattern: '\\b(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)(\\.(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)){3}\\b', color: 'blue', enabled: true, ignoreCase: false }
  ];
  function listHighlightRules() { return Promise.resolve(lsGet('hlrules', DEFAULT_HL)); }
  function saveHighlightRule(rule) {
    var list = lsGet('hlrules', DEFAULT_HL);
    var i = list.findIndex(function (x) { return x.id === rule.id; });
    if (i >= 0) list[i] = rule; else list.push(rule);
    lsSet('hlrules', list);
    return Promise.resolve(rule);
  }
  function deleteHighlightRule(id) {
    lsSet('hlrules', lsGet('hlrules', DEFAULT_HL).filter(function (x) { return x.id !== id; }));
    return Promise.resolve();
  }

  // 主题
  var DEFAULT_THEME = {
    name: 'BastionShell Light（默认）', background: '#ffffff', foreground: '#333333', cursor: '#007acc',
    selectionBackground: 'rgba(0, 122, 204, 0.25)',
    ansi: ['#000000','#cd3131','#107c10','#949800','#0451a5','#bc05bc','#0598bc','#555555','#666666','#cd3131','#14ce14','#b5ba00','#0451a5','#bc05bc','#0598bc','#a5a5a5']
  };
  var DARK_THEME = {
    name: 'BastionShell Dark', background: '#12121a', foreground: '#e4e4ef', cursor: '#8ab4ff',
    selectionBackground: 'rgba(90, 120, 255, 0.35)',
    ansi: ['#000000','#cd3131','#0dbc79','#e5e510','#2472c8','#bc3fbc','#11a8cd','#e5e5e5','#666666','#f14c4c','#23d18b','#f5f543','#3b8eea','#d670d6','#29b8db','#e5e5e5']
  };
  function getTheme() { return Promise.resolve(lsGet('theme', DEFAULT_THEME)); }
  function resetTheme() { lsSet('theme', DEFAULT_THEME); return Promise.resolve(DEFAULT_THEME); }
  function builtinDarkTheme() { lsSet('theme', DARK_THEME); return Promise.resolve(DARK_THEME); }
  function importTheme() { return Promise.resolve(null); }

  // 转发规则
  function listForwardRules(profileId) {
    return Promise.resolve(lsGet('fwrules', []).filter(function (r) { return r.profileId === profileId; }));
  }
  function saveForwardRule(profileId, rule) {
    rule.profileId = profileId;
    var list = lsGet('fwrules', []);
    var i = list.findIndex(function (x) { return x.id === rule.id; });
    if (i >= 0) list[i] = rule; else list.push(rule);
    lsSet('fwrules', list);
    return Promise.resolve(rule);
  }
  function deleteForwardRule(ruleId) {
    lsSet('fwrules', lsGet('fwrules', []).filter(function (x) { return x.id !== ruleId; }));
    return Promise.resolve();
  }
  function startForward(connectionId, rule) {
    var ws = controlWS[connectionId];
    if (!ws || ws.readyState !== 1) return Promise.resolve({ ok: false, message: '连接未建立' });
    fwOwner[rule.id] = connectionId;
    return new Promise(function (resolve) {
      pendingFw[rule.id] = { resolve: resolve };
      emit('forwardStatus', { ruleId: rule.id, status: 'idle' });
      ws.send(JSON.stringify({
        type: 'forward:start', id: rule.id,
        localHost: rule.localHost, localPort: rule.localPort,
        remoteHost: rule.remoteHost, remotePort: rule.remotePort
      }));
    });
  }
  function stopForward(ruleId) {
    var connId = fwOwner[ruleId];
    var ws = connId && controlWS[connId];
    delete fwOwner[ruleId];
    if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: 'forward:stop', id: ruleId }));
    return Promise.resolve();
  }

  // ---- 部署执行 ----
  function pickFiles() {
    return fetch(API_BASE + '/api/pick-files')
      .then(function (r) { return r.json(); })
      .then(function (res) { return (res && res.paths) || []; })
      .catch(function () { return []; });
  }

  // ---- 其它 ----
  function answerAuth(nonce, value) {
    var connId = authConnByNonce[nonce];
    var ws = connId && controlWS[connId];
    if (ws && ws.readyState === 1) {
      delete authConnByNonce[nonce];
      ws.send(JSON.stringify({ type: 'auth-answer', nonce: nonce, value: value === null ? '' : value }));
      return Promise.resolve(true);
    }
    return Promise.resolve(false);
  }
  function statPath(path) {
    var f = fileStore[path];
    if (f) return Promise.resolve({ isDir: false, size: f.size });
    return Promise.resolve({ isDir: false, size: 0 });
  }
  function syncDirectory(req) { return Promise.resolve({ ok: false, rc: -1, message: '浏览器模式暂不支持目录同步' }); }
  function cancelTransfer(sessionId) {
    var p = pendingUpload[sessionId];
    if (p) {
      delete pendingUpload[sessionId];
      p.reject(new Error('已取消'));
      emit('zmodemEvent', { sessionId: sessionId, transferId: p.transferId, direction: 'send', type: 'error', message: '已取消' });
    }
  }
  function getPathForFile(file) {
    var token = 'file-' + (++seq);
    fileStore[token] = file;
    return token;
  }
  function pickPrivateKey() {
    // 浏览器拿不到文件绝对路径，改为读文件内容（PEM 文本），后端 loadKey 识别内容
    return new Promise(function (resolve) {
      var input = document.createElement('input');
      input.type = 'file';
      var settled = false;
      function done(v) { if (!settled) { settled = true; resolve(v); } }
      input.onchange = function () {
        var f = input.files && input.files[0];
        if (!f) { done(null); return; }
        var reader = new FileReader();
        reader.onload = function () { done(String(reader.result || '')); };
        reader.onerror = function () { done(null); };
        reader.readAsText(f);
      };
      var onFocus = function () {
        // 文件对话框关闭后窗口重新聚焦；若还没选到文件则视为取消
        setTimeout(function () { done(null); }, 400);
        window.removeEventListener('focus', onFocus);
      };
      window.addEventListener('focus', onFocus);
      input.click();
    });
  }
  function openExternal(url) { try { window.open(url, '_blank'); } catch (e) {} return Promise.resolve(true); }
  function logToMain(level, message) { try { console.log('[' + level + ']', message); } catch (e) {} }
  function readClipboard() {
    try { return navigator.clipboard.readText(); }
    catch (e) { return Promise.resolve(''); }
  }
  function writeClipboard(text) {
    try { navigator.clipboard.writeText(text); } catch (e) {}
  }

  window.bastion = {
    connect: connect,
    answerAuth: answerAuth,
    closeConnection: closeConnection,
    diagnose: diagnose,
    openSession: openSession,
    writeSession: writeSession,
    resizeSession: resizeSession,
    closeSession: closeSession,
    sendFiles: sendFiles,
    statPath: statPath,
    syncDirectory: syncDirectory,
    cancelTransfer: cancelTransfer,
    getPathForFile: getPathForFile,
    listQuickCommands: listQuickCommands,
    saveQuickCommand: saveQuickCommand,
    deleteQuickCommand: deleteQuickCommand,
    sendCommand: sendCommand,
    listHighlightRules: listHighlightRules,
    saveHighlightRule: saveHighlightRule,
    deleteHighlightRule: deleteHighlightRule,
    getTheme: getTheme,
    importTheme: importTheme,
    resetTheme: resetTheme,
    builtinDarkTheme: builtinDarkTheme,
    openExternal: openExternal,
    pickPrivateKey: pickPrivateKey,
    readClipboard: readClipboard,
    writeClipboard: writeClipboard,
    listProfiles: listProfiles,
    saveProfile: saveProfile,
    deleteProfile: deleteProfile,
    listForwardRules: listForwardRules,
    saveForwardRule: saveForwardRule,
    deleteForwardRule: deleteForwardRule,
    startForward: startForward,
    stopForward: stopForward,
    pickFiles: pickFiles,
    logToMain: logToMain,
    onAuthPrompt: function (cb) { return on('authPrompt', cb); },
    onConnectionStatus: function (cb) { return on('connectionStatus', cb); },
    onRestored: function (cb) { return on('restored', cb); },
    onSessionData: function (cb) { return on('sessionData', cb); },
    onSessionClosed: function (cb) { return on('sessionClosed', cb); },
    onHostKey: function (cb) { return on('hostKey', cb); },
    onZmodemEvent: function (cb) { return on('zmodemEvent', cb); },
    onForwardStatus: function (cb) { return on('forwardStatus', cb); }
  };
})();
