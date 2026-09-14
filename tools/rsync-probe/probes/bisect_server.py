#!/usr/bin/env python3
"""bisect_server.py —— 路线 A 的二分仪器：**真客户端** + 可控传输（pipe / pty）。

为什么要自己写一个（而不是直接用搬过来的 bridge_server.py）
------------------------------------------------------------
搬过来的 `bridge_server.py` 只有 pty 一种传输，而且它的计数器显示「两个方向都 0 字节」，
既看不出是**传输**的问题还是**桥的接线**的问题。旧脚本从来没有把这两个变量分开测过
（`rsync_pipe_control.py` 和 `rsync_spike_stage1.py` 都是「两头都用 --server」，
没有一端是真客户端）。这里把它拆开：

    真客户端(rsync) --[ -e shim ]--> unix socket --+
                                                   +--> pipe: rsync --server   （干净字节流）
                                                   +--> pty : bash --norc -> 注入 rsync --server

判读
----
    pipe 通、pty 不通 → 问题在 pty 这一层（终端模式 / 二进制透传）
    两个都不通        → 桥的接线或握手顺序有问题，**与堡垒机无关**
    pipe 通了且 pty 也通 → 桥本身没问题，只剩堡垒机那一腿没验

用法
----
    python3 probes/bisect_server.py --transport pipe                 # 用客户端发来的原选项串
    python3 probes/bisect_server.py --transport pipe --opts nov      # 剥掉 v
    python3 probes/bisect_server.py --transport pty
    python3 probes/bisect_server.py --transport pty  --opts nov
"""

import argparse
import os
import pty
import select
import shutil
import socket
import subprocess
import sys
import threading
import time
import tty

HERE = os.path.dirname(os.path.abspath(__file__))
SHIM = os.path.join(HERE, 'bisect_shim.py')
BASE = '/tmp/rs-bisect'
MARK = b'__BISECT_DONE_'


def prepare(tag):
    root = os.path.join(BASE, tag)
    src = os.path.join(root, 'src')
    dst = os.path.join(root, 'dst')
    shutil.rmtree(root, ignore_errors=True)
    os.makedirs(src)
    os.makedirs(dst)
    with open(os.path.join(src, 'big.bin'), 'wb') as f:
        f.write(os.urandom(256 * 1024))
    with open(os.path.join(src, 'a.txt'), 'w') as f:
        f.write('hello bisect\n')
    return src, dst


def same(src, dst):
    a, b = os.path.join(src, 'big.bin'), os.path.join(dst, 'big.bin')
    if not (os.path.exists(a) and os.path.exists(b)):
        return False
    with open(a, 'rb') as fa, open(b, 'rb') as fb:
        return fa.read() == fb.read()


def pick_opts(remote_cmd, mode):
    """从客户端发来的远端命令里取出那个紧凑选项串（形如 -logDtpre.iLsfxCIvu）"""
    opts = ''
    for tok in remote_cmd.split():
        if tok.startswith('-') and tok != '--server' and not tok.startswith('--'):
            opts = tok
    if mode == 'passthrough':
        return opts
    if mode == 'nov':
        return opts.replace('v', '')      # 只摘 v
    if mode == 'novu':
        return opts.replace('v', '').replace('u', '')
    return opts


class Diag:
    def __init__(self):
        self.c2r = 0          # 客户端 → 远端
        self.r2c = 0          # 远端 → 客户端
        self.c2r_head = bytearray()
        self.r2c_head = bytearray()
        self.r2c_tail = bytearray()   # 最后 64 字节：看协议是"收完了"还是"断在中间"
        self.c2r_tail = bytearray()
        self.mark = False
        self.raw_seen = False

    def note(self, key, data):
        if key == 'c2r':
            self.c2r += len(data)
            if len(self.c2r_head) < 160:
                self.c2r_head.extend(data[:160 - len(self.c2r_head)])
            self.c2r_tail.extend(data)
            del self.c2r_tail[:-64]
        else:
            self.r2c += len(data)
            if len(self.r2c_head) < 160:
                self.r2c_head.extend(data[:160 - len(self.r2c_head)])
            self.r2c_tail.extend(data)
            del self.r2c_tail[:-64]
            if MARK in data:
                self.mark = True


def _proc_read(path):
    try:
        with open(path) as f:
            return f.read().strip()
    except OSError:
        return ''


def snapshot(pid, label):
    """超时现场：进程卡在哪个系统调用 / 开着哪些 fd / 子进程在干什么。

    不猜，直接看 —— 「客户端不退出」这种问题，猜是最容易猜错的（我这一路已经猜错两次）。
    """
    status = _proc_read('/proc/%d/status' % pid)
    state = next((l for l in status.splitlines() if l.startswith('State:')), 'State: ?')
    wchan = _proc_read('/proc/%d/wchan' % pid) or '?'
    print('SNAP[%s] pid=%s %s wchan=%s' % (label, pid, state, wchan), flush=True)
    fds = []
    try:
        for fd in sorted(os.listdir('/proc/%d/fd' % pid), key=int):
            try:
                fds.append('%s->%s' % (fd, os.readlink('/proc/%d/fd/%s' % (pid, fd))))
            except OSError:
                pass
    except OSError:
        pass
    print('  FDS: %s' % '  '.join(fds), flush=True)

    for entry in os.listdir('/proc'):
        if not entry.isdigit():
            continue
        st = _proc_read('/proc/%s/status' % entry)
        m = next((l for l in st.splitlines() if l.startswith('PPid:')), '')
        if m.split()[-1:] == [str(pid)]:
            cstate = next((l for l in st.splitlines() if l.startswith('State:')), 'State: ?')
            cw = _proc_read('/proc/%s/wchan' % entry) or '?'
            cmd = _proc_read('/proc/%s/cmdline' % entry).replace('\x00', ' ')
            cfds = []
            try:
                for fd in sorted(os.listdir('/proc/%s/fd' % entry), key=int):
                    try:
                        cfds.append('%s->%s' % (fd, os.readlink('/proc/%s/fd/%s' % (entry, fd))))
                    except OSError:
                        pass
            except OSError:
                pass
            print('  CHILD pid=%s %s wchan=%s' % (entry, cstate, cw), flush=True)
            print('    CMD: %s' % cmd[:160], flush=True)
            print('    FDS: %s' % '  '.join(cfds), flush=True)


class PrefixFilter:
    """找到**协议握手的那一个字节**再开始转发，而不是"丢掉看着像噪声的字符"。

    实测（2026-09-11）客户端最先收到的不是协议，而是终端噪声：
      Ubuntu 3.2.7 : `\\x1b[?2004l\\r\\n` + 协议号 0x1f(31)
      AlmaLinux 3.4.4: `\\x1b[?2004l\\r\\n` + 协议号 **0x20(32)**
    客户端把噪声当成协议版本 → `protocol version mismatch -- is your shell clean?`

    规则：跳过开头的 ESC 转义序列与 CR/LF，然后**扫描到「0x1c..0x22 的字节 + 00 00 00」**
    （那就是 rsync 的二进制握手：协议号 4 字节小端）。找到之后，后面一个字节都不动。

    ⚠️ 踩过的两个坑（都在这儿踩的，记下来免得再犯）：
      1. 只在 0x40..0x7e 里找 CSI 结束字节 → `[`(0x5b) 自己就"结束"了序列，
         于是 `\\x1b[?2004l` 变成留下 `?2004l`（噪声依旧）。
      2. 把 `0x20` 当空白丢掉 → **吃掉了协议号 32**，AlmaLinux 上直接 mismatch。
         协议号 0x20 和空格长得一模一样，靠"值"猜噪声是不行的，必须**按握手形状**认。
    """

    GREETING_MIN, GREETING_MAX = 0x1c, 0x22   # 协议号大致范围（29..34）
    SCAN_LIMIT = 4096

    def __init__(self):
        self.done = False
        self.buf = bytearray()
        self.dropped = 0

    def feed(self, data):
        if self.done:
            return data
        self.buf += data
        i = 0
        n = len(self.buf)
        while i < n:
            b = self.buf[i]
            if b == 0x1b:                                  # ESC 序列：整体跳过
                j = i + 1
                if j < n and self.buf[j] == 0x5b:           # CSI：参数/中间字节 0x20..0x3f
                    j += 1
                    while j < n and 0x20 <= self.buf[j] <= 0x3f:
                        j += 1
                else:
                    while j < n and not (0x40 <= self.buf[j] <= 0x7e):
                        j += 1
                if j >= n:
                    self.dropped += i
                    del self.buf[:i]
                    return b''
                i = j + 1
                continue
            if b in (0x0d, 0x0a):
                i += 1
                continue
            if self.GREETING_MIN <= b <= self.GREETING_MAX:
                if i + 4 > n:                              # 还看不出是不是握手，等更多字节
                    break
                if self.buf[i + 1:i + 4] == b'\x00\x00\x00':
                    self.done = True
                    self.dropped += i
                    out = bytes(self.buf[i:])
                    self.buf.clear()
                    return out
            if i > self.SCAN_LIMIT:                        # 扫太久还没找到：放弃过滤，原样放行
                self.done = True
                self.dropped += i
                out = bytes(self.buf)
                self.buf.clear()
                return out
            i += 1                                         # 其它字节当噪声跳过，继续找
        self.dropped += i
        del self.buf[:i]
        return b''


def write_all(dst, data):
    """往「文件对象 / socket / 裸 fd」三个世界里写 —— 三者写法不一样（socket 没有 os.write）"""
    if hasattr(dst, 'sendall'):
        dst.sendall(data)
    elif hasattr(dst, 'write'):
        dst.write(data)
        dst.flush()
    else:
        os.write(dst, data)


def pump(src, dst, key, diag, stop, on_eof=None):
    """把一个方向搬字节。EOF/出错时调 on_eof —— 这一步很关键：
    不把 EOF 传下去，对面就会一直等（看起来像"卡死"，其实是没人告诉它结束了）。"""
    while not stop.is_set():
        try:
            r, _, _ = select.select([src], [], [], 0.2)
        except (OSError, ValueError):
            break
        if not r:
            continue
        try:
            d = os.read(src.fileno() if hasattr(src, 'fileno') else src, 65536)
        except OSError:
            break
        if not d:
            break
        diag.note(key, d)
        try:
            write_all(dst, d)
        except (BrokenPipeError, OSError):
            break
    if on_eof and not stop.is_set():
        try:
            on_eof()
        except Exception:
            pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--transport', choices=['pipe', 'pty', 'local'], required=True,
                    help='local = 没有 socket / 没有 shim 的对照：-e 直接指向一个 exec rsync 的脚本')
    ap.add_argument('--opts', choices=['passthrough', 'nov', 'novu'], default='passthrough')
    ap.add_argument('--shell', choices=['bash', 'exec'], default='bash',
                    help='pty 上怎么起远端：bash=交互式 bash 里注入命令（堡垒机的样子）；'
                         'exec=p tys 上直接 sh -c exec（没有 readline，用来反证"噪声来自 shell"）')
    ap.add_argument('--filter-prefix', action='store_true',
                    help='把协议开始之前的终端噪声丢掉（见 PrefixFilter）')
    ap.add_argument('--timeout', type=float, default=25.0)
    args = ap.parse_args()

    tag = '%s_%s' % (args.transport, args.opts)
    src, dst = prepare(tag)
    rsync_ver = subprocess.run(['rsync', '--version'], capture_output=True, text=True).stdout.splitlines()[0]
    print('ENV %s' % rsync_ver, flush=True)
    print('RUN transport=%s opts=%s timeout=%ss' % (args.transport, args.opts, args.timeout), flush=True)

    # 本地对照：`-e` 指向一个只用 exec 起 rsync --server 的脚本。
    # 没有 python shim、没有 unix socket —— 用来判断「不退出」到底是 rsync 的收尾期望，
    # 还是我们自己搭的 socket/shim 那一层的问题。
    if args.transport == 'local':
        local_opts = {'passthrough': '-logDtpre.iLsfxCIvu',
                      'nov': '-logDtpre.iLsfxCIu',
                      'novu': '-logDtpre.iLsfxC'}[args.opts]
        rsh = '/tmp/bisect_local_rsh_%d.sh' % os.getpid()
        with open(rsh, 'w') as f:
            f.write('#!/bin/sh\nexec rsync --server %s . %s/\n' % (local_opts, dst))
        os.chmod(rsh, 0o755)
        print('RSH=%s (内容: exec rsync --server %s . %s/)' % (rsh, local_opts, dst), flush=True)
        t0 = time.time()
        client = subprocess.Popen(['rsync', '-a', '-e', rsh, src + '/', 'host:' + dst + '/'],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            rc = client.wait(timeout=args.timeout)
            result = 'rc=%s' % rc
        except subprocess.TimeoutExpired:
            print('--- 超时现场 ---', flush=True)
            snapshot(client.pid, 'client')
            client.kill()
            result = 'TIMEOUT'
        print('RESULT=%s secs=%s' % (result, round(time.time() - t0, 1)), flush=True)
        print('MATCH=%s' % same(src, dst), flush=True)
        err = (client.stderr.read() or b'').decode('utf-8', 'replace').strip()
        print('CLIENT_STDERR=%s' % (err[:600] if err else '(空)'), flush=True)
        print('DONE', flush=True)
        return

    sock_path = '/tmp/rs-bisect-%d.sock' % os.getpid()
    try:
        os.unlink(sock_path)
    except OSError:
        pass

    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(sock_path)
    srv.listen(1)

    env = dict(os.environ)
    env['BRIDGE_SOCK'] = sock_path
    env['BRIDGE_DEBUG'] = '1'
    # ⚠️ 必须把 shim 拷到一个**不带空格**的路径：rsync 的 `-e` 是按空格切分的，
    # 而这个探针目录的绝对路径里就有空格（.../DeepSeek Harness/...），
    # 直接引用会被切成两段 → 客户端根本起不来（表现为「没连上桥」，很难查）。
    # 当年 bridge_server.py 把 bridge_client.py 拷到 /tmp 也是同一个原因。
    shim_path = '/tmp/bisect_shim_%d.py' % os.getpid()
    shutil.copyfile(SHIM, shim_path)
    if ' ' in shim_path:
        print('ABORT: shim 路径里还有空格，rsync -e 会被切坏', flush=True)
        return

    client = subprocess.Popen(
        ['rsync', '-a', '-e', 'python3 %s' % shim_path, src + '/', 'host:' + dst + '/'],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, env=env)
    print('SHIM=%s' % shim_path, flush=True)

    srv.settimeout(args.timeout)
    try:
        conn, _ = srv.accept()
    except socket.timeout:
        client.kill()
        err = (client.stderr.read() or b'').decode('utf-8', 'replace').strip()
        print('RESULT=NO_CONNECT（客户端根本没连上桥）', flush=True)
        print('CLIENT_RC=%s' % client.returncode, flush=True)
        print('CLIENT_STDERR=%s' % (err[:600] if err else '(空)'), flush=True)
        print('DONE', flush=True)
        return
    srv.close()

    buf = b''
    while b'\n' not in buf:
        d = conn.recv(4096)
        if not d:
            break
        buf += d
    remote_cmd, _, leftover = buf.partition(b'\n')
    remote_cmd = remote_cmd.decode('utf-8', 'replace')
    opts = pick_opts(remote_cmd, args.opts)
    print('REMOTE_CMD=%s' % remote_cmd, flush=True)
    print('CLIENT_OPTS=%s -> USED=%s' % (pick_opts(remote_cmd, 'passthrough'), opts), flush=True)
    print('LEFTOVER_BEFORE_INJECT=%d bytes' % len(leftover), flush=True)

    diag = Diag()
    stop = threading.Event()
    remote = None

    if args.transport == 'pipe':
        remote = subprocess.Popen(['rsync', '--server', opts, '.', dst + '/'],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        t1 = threading.Thread(target=pump, args=(conn, remote.stdin, 'c2r', diag, stop),
                              kwargs={'on_eof': lambda: remote.stdin.close()}, daemon=True)
        t2 = threading.Thread(target=pump, args=(remote.stdout, conn, 'r2c', diag, stop),
                              kwargs={'on_eof': lambda: conn.shutdown(socket.SHUT_WR)}, daemon=True)
        t1.start()
        t2.start()
    else:
        master, slave = pty.openpty()
        tty.setraw(slave)
        flt = PrefixFilter() if args.filter_prefix else None
        if args.shell == 'exec':
            # 反证用：pty 上直接 exec，没有交互式 shell，就没有 readline 的噪声
            shell = subprocess.Popen(
                ['/bin/sh', '-c', 'exec rsync --server %s . %s/' % (opts, dst)],
                stdin=slave, stdout=slave, stderr=slave, close_fds=True)
            os.close(slave)
        else:
            shell = subprocess.Popen(['/bin/bash', '--norc', '--noprofile'],
                                     stdin=slave, stdout=slave, stderr=slave, close_fds=True)
            os.close(slave)
            time.sleep(0.5)
            # 抽掉启动横幅，免得混进协议流
            while select.select([master], [], [], 0.2)[0]:
                try:
                    os.read(master, 4096)
                except OSError:
                    break
            remote_line = ('stty raw -echo -iexten 2>/dev/null; PS1=; echo __BISECT_RAW__; '
                           'rsync --server %s . %s/ '
                           '2>/tmp/rs-bisect-remote-err.txt; rc=$?; stty sane 2>/dev/null; '
                           'echo __BISECT_DONE_${rc}__\r' % (opts, dst))
            os.write(master, remote_line.encode())

        def r2c_out(data):
            # 看到完成标记就**不再转发**（它是我方的控制信号，不是协议数据），
            # 然后把连接写半边关掉 —— 这一步是「传完之后客户端才肯退出」的关键：
            # 不把 EOF 传下去，rsync 会一直等对面说再见（表现为文件已传完但进程不退出）。
            if b'__BISECT_RAW__' in data:
                diag.raw_seen = True
            if MARK in data:
                diag.note('r2c', data.split(MARK)[0])
                write_all(conn, data.split(MARK)[0])
                diag.mark = True
                try:
                    conn.shutdown(socket.SHUT_WR)
                except OSError:
                    pass
                return
            # filter-prefix：只过滤开头那段终端噪声，协议开始后一个字节都不动
            if flt is not None:
                data = flt.feed(data)
                if not data:
                    return
            diag.note('r2c', data)
            write_all(conn, data)

        def r2c_pump():
            while not stop.is_set():
                try:
                    r, _, _ = select.select([master], [], [], 0.2)
                except (OSError, ValueError):
                    break
                if not r:
                    continue
                try:
                    d = os.read(master, 65536)
                except OSError:
                    break
                if not d:
                    break
                r2c_out(d)
            if not stop.is_set():
                try:
                    conn.shutdown(socket.SHUT_WR)
                except OSError:
                    pass

        t1 = threading.Thread(target=pump, args=(conn, master, 'c2r', diag, stop), daemon=True)
        t2 = threading.Thread(target=r2c_pump, daemon=True)
        t1.start()
        t2.start()
        remote = shell

    t0 = time.time()
    try:
        rc = client.wait(timeout=args.timeout)
        result = 'rc=%s' % rc
    except subprocess.TimeoutExpired:
        # 超时先别杀：把现场拍下来（谁卡在什么系统调用、开着哪些 fd、协议收到哪儿了）
        print('--- 超时现场 ---', flush=True)
        snapshot(client.pid, 'client')
        if remote and remote.poll() is None:
            snapshot(remote.pid, 'remote')
        elif remote:
            print('SNAP[remote] pid=%s 已退出 rc=%s' % (remote.pid, remote.poll()), flush=True)
        print('TAIL client->remote = %s' % bytes(diag.c2r_tail).hex(), flush=True)
        print('TAIL remote->client = %s' % bytes(diag.r2c_tail).hex(), flush=True)
        client.kill()
        result = 'TIMEOUT'
    secs = round(time.time() - t0, 1)
    stop.set()
    time.sleep(0.3)

    try:
        if remote and remote.poll() is None:
            remote.kill()
    except Exception:
        pass

    print('RESULT=%s secs=%s  client->remote=%d  remote->client=%d  mark=%s  raw_mark=%s' % (
        result, secs, diag.c2r, diag.r2c, diag.mark, diag.raw_seen), flush=True)
    print('MATCH=%s' % same(src, dst), flush=True)
    cerr = (client.stderr.read() or b'').decode('utf-8', 'replace').strip()
    if cerr:
        print('CLIENT_STDERR=%s' % cerr[:600], flush=True)
    print('C2R_HEAD=%s' % diag.c2r_head.hex(), flush=True)
    print('R2C_HEAD=%s' % diag.r2c_head.hex(), flush=True)
    if args.filter_prefix:
        print('PREFIX_DROPPED=%d bytes' % flt.dropped, flush=True)
    if args.transport == 'pty':
        try:
            with open('/tmp/rs-bisect-remote-err.txt') as f:
                print('REMOTE_STDERR=%s' % f.read().strip()[:600], flush=True)
        except OSError:
            print('REMOTE_STDERR=(文件不存在 → 注入的那条命令没跑起来)', flush=True)
    print('DONE', flush=True)


if __name__ == '__main__':
    main()
