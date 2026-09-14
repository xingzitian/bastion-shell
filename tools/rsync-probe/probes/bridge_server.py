#!/usr/bin/env python3
"""bridge_server.py v4 — 协议 32 + 剥离 'v' 标志（禁用 do_negotiated_strings）。

死锁根因：rsync 3.2+ 的选项串 -logDtpre.iLsfxCIvu 里的 'v' 触发
CF_VARINT_FLIST_FLAGS + do_negotiated_strings（vstring 协商），在手动桥接场景卡死。
注入时去掉 'v' → 服务端不设该位 → 客户端读到 compat_flags 无此位 → 双方都不做 vstring，
等效于老协议（生产目标机 rsync 3.1.x 本就没有该协商）。
"""
import os
import pty
import select
import shutil
import socket
import subprocess
import threading
import time

SRC = '/tmp/rs-src'
DST = '/tmp/rs-dst'
SOCK = '/tmp/rsync_bridge.sock'
GREETING = b'\x20\x00\x00\x00'  # 二进制握手开头（协议号 32）
MARK = b'__RSYNC_DONE_'


def drain(master, dur):
    end = time.time() + dur
    while time.time() < end:
        r, _, _ = select.select([master], [], [], 0.05)
        if r:
            try:
                os.read(master, 4096)
            except OSError:
                break


def main():
    print('START', flush=True)
    shutil.rmtree(SRC, ignore_errors=True)
    shutil.rmtree(DST, ignore_errors=True)
    os.makedirs(SRC)
    os.makedirs(DST)
    open(os.path.join(SRC, 'big.bin'), 'wb').write(os.urandom(1024 * 1024))
    open(os.path.join(SRC, 'a.txt'), 'w').write('hello v1\n')

    master, slave = pty.openpty()
    bash = subprocess.Popen(['/bin/bash', '--norc', '--noprofile'],
                            stdin=slave, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)
    time.sleep(0.5)
    drain(master, 0.5)

    try:
        os.unlink(SOCK)
    except OSError:
        pass
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(SOCK)
    srv.listen(1)

    local = subprocess.Popen(
        ['rsync', '-a', '-e', 'python3 /tmp/bridge_client.py', SRC + '/', 'host:' + DST + '/'],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)

    conn, _ = srv.accept()
    buf = b''
    while b'\n' not in buf:
        d = conn.recv(4096)
        if not d:
            break
        buf += d
    # 用 partition 保留 \n 之后的残留字节（客户端的握手可能跟命令同包到达）
    head, _, leftover = buf.partition(b'\n')
    remote_cmd = head.decode('utf-8', 'replace')
    print('REMOTE_CMD=', remote_cmd, flush=True)
    print('LEFT_OVER_HEX=', leftover.hex(), flush=True)

    # 关键：剥离选项串里的 'v'（禁用 negotiated strings），等效老协议
    forced_cmd = remote_cmd.replace('-logDtpre.iLsfxCIvu', '-logDtpre.iLsfxCIu')
    print('FORCED_CMD=', forced_cmd, flush=True)
    remote_line = ('stty raw -echo -iexten 2>/dev/null; PS1=; ' + forced_cmd +
                   ' 2>/tmp/rsync_remote_err.txt; rc=$?; stty sane 2>/dev/null; echo __RSYNC_DONE_${rc}__\r')
    os.write(master, remote_line.encode())

    greeting_seen = threading.Event()
    diag = {'fwd': 0, 'l2r': 0, 'l2r_hex': bytearray(), 'mark': False, 'tail_hex': b'', 'fwd_hex': bytearray()}

    def pty_to_conn():
        buf = b''
        seen = False
        tail = b''
        while True:
            r, _, _ = select.select([master], [], [], 0.5)
            if not r:
                if local.poll() is not None:
                    break
                continue
            try:
                d = os.read(master, 4096)
            except OSError:
                break
            if not d:
                break
            if not seen:
                buf += d
                i = buf.find(GREETING)
                if i >= 0:
                    greeting_seen.set()
                    print('SERVER_GREETING_FOUND_AT=', i, 'BUF_HEX=', buf[:40].hex(), flush=True)
                    try:
                        conn.sendall(buf[i:])
                        diag['fwd'] += len(buf[i:])
                        diag['fwd_hex'].extend(buf[i:])
                    except OSError:
                        break
                    buf = b''
                    seen = True
                continue
            data = tail + d
            i = data.find(MARK)
            if i >= 0:
                diag['mark'] = True
                # 从标记 __RSYNC_DONE_<rc>__ 里解析接收端退出码
                rest = data[i + len(MARK):]
                j = rest.find(b'__')
                if j >= 0:
                    try:
                        diag['rc'] = int(rest[:j])
                    except ValueError:
                        diag['rc'] = -1
                try:
                    conn.sendall(data[:i])
                    diag['fwd'] += i
                except OSError:
                    pass
                break
            # 只保留"可能是 MARK 前缀"的后缀，其余立即转发（避免扣住 compat_flags 等小数据）
            keep = 0
            for k in range(1, len(MARK)):
                if len(data) >= k and data[-k:] == MARK[:k]:
                    keep = k
            if keep == 0:
                try:
                    conn.sendall(data)
                    diag['fwd'] += len(data)
                    diag['fwd_hex'].extend(data)
                except OSError:
                    break
                tail = b''
            else:
                try:
                    conn.sendall(data[:-keep])
                    diag['fwd'] += len(data) - keep
                    diag['fwd_hex'].extend(data[:-keep])
                except OSError:
                    break
                tail = data[-keep:]
        try:
            conn.shutdown(socket.SHUT_WR)
        except OSError:
            pass

    def conn_to_pty():
        if not greeting_seen.wait(timeout=20):
            return
        # 先转交残留的客户端字节（同包到达的握手），再继续读 conn
        pending = leftover
        while True:
            if pending:
                d = pending
                pending = b''
            else:
                r, _, _ = select.select([conn], [], [], 0.5)
                if not r:
                    if local.poll() is not None:
                        break
                    continue
                try:
                    d = conn.recv(65536)
                except OSError:
                    break
                if not d:
                    break
            diag['l2r'] += len(d)
            if len(diag['l2r_hex']) < 32:
                diag['l2r_hex'].extend(d[:32 - len(diag['l2r_hex'])])
            try:
                os.write(master, d)
            except OSError:
                break

    t1 = threading.Thread(target=pty_to_conn, daemon=True)
    t2 = threading.Thread(target=conn_to_pty, daemon=True)
    t1.start()
    t2.start()

    try:
        rc = local.wait(timeout=30)
    except subprocess.TimeoutExpired:
        local.kill()
        rc = 'TIMEOUT'
    t1.join(timeout=3)
    t2.join(timeout=3)

    # 干净退出：接收端已退出（DONE 标记出现）→ 文件已写完，本地客户端只是等多余的 NDX_DONE 回显
    # 以标记里的接收端退出码为准，强制结束本地客户端
    final_rc = rc
    if rc == 'TIMEOUT' and diag.get('mark'):
        local.kill()
        final_rc = diag.get('rc', 0)
    print('LOCAL_RC=', rc, '=> FINAL_RC=', final_rc, flush=True)
    print('fwd_bytes=', diag['fwd'], 'l2r_bytes=', diag['l2r'], 'mark_found=', diag['mark'], flush=True)
    print('FWD_HEX=', bytes(diag['fwd_hex']).hex(), flush=True)
    print('LOCAL_STDERR=', local.stderr.read().decode('utf-8', 'replace'), flush=True)
    try:
        with open('/tmp/rsync_remote_err.txt') as f:
            print('REMOTE_STDERR=', f.read(), flush=True)
    except OSError:
        print('REMOTE_STDERR=(no file)', flush=True)
    try:
        with open('/tmp/stty_state.txt') as f:
            print('STTY_STATE=', f.read().strip(), flush=True)
    except OSError:
        print('STTY_STATE=(no file)', flush=True)
    if os.path.exists(os.path.join(DST, 'big.bin')):
        print('MATCH=', open(os.path.join(SRC, 'big.bin'), 'rb').read() ==
              open(os.path.join(DST, 'big.bin'), 'rb').read(), flush=True)
    else:
        print('DST big.bin missing', flush=True)
    bash.terminate()
    print('DONE', flush=True)


if __name__ == '__main__':
    main()
