#!/usr/bin/env python3
"""v 标志 × 传输方式 的对照矩阵（本地，不需要堡垒机）。

为什么写这个
------------
`bridge_server.py` 的 docstring 记着一条根因：「选项串里的 'v' 触发 vstring 协商，
在手动桥接场景卡死」。但那是一条**说法** —— 当年验证它的脚本已经不在了
（或者从来没写成一次能重跑的实验）。这里把那条说法变成**可重跑的实测**：

  假设 H1：在 raw pty 上跑 `rsync --server` 时，选项串含 'v' 会卡死（超时）；
  假设 H2：同一个 pty，把 'v' 去掉就能跑通；
  对照   ：干净管道（无 pty）上，两种选项串都应当跑通。

顺带把 'u' 也单独列出来 —— 原始对照组 `rsync_pipe_control.py` 用的是
`-logDtpre.iLsfxCIvu`，而 `rsync_spike_stage1.py` 用的是 `-logDtpre.iLsfxC`，
两者差了 `vu` 两个字符，并没有把 'v' 单独隔离出来。要做实验就得把变量分开。

说明（关于选项串）
------------------
这里用的是「客户端会发给 --server 的那串紧凑选项」，照抄自当年那两个探针，
只在末尾做减法。`-vvv` 这类客户端冗余度没带进来，减少变量。

用法
----
    python3 probes/vflag_matrix.py            # 默认每格 20 秒超时
    python3 probes/vflag_matrix.py --timeout 30
"""

import argparse
import os
import pty
import select
import shutil
import subprocess
import threading
import time
import tty

BASE = '/tmp/rs-vflag'

# 三种选项串：把 'v'（和 'u'）逐个摘掉，别让两个变量混在一起
VARIANTS = [
    ('full   (Ivu)', '-logDtpre.iLsfxCIvu'),
    ('no_v   (Iu )', '-logDtpre.iLsfxCIu'),
    ('no_vu  (I  )', '-logDtpre.iLsfxCI'),
]


def prepare(tag):
    """造一棵源目录：一个 1 MiB 随机文件（二进制）+ 一个文本文件"""
    src = os.path.join(BASE, tag, 'src')
    dst = os.path.join(BASE, tag, 'dst')
    shutil.rmtree(os.path.join(BASE, tag), ignore_errors=True)
    os.makedirs(src)
    os.makedirs(dst)
    with open(os.path.join(src, 'big.bin'), 'wb') as f:
        f.write(os.urandom(1024 * 1024))
    with open(os.path.join(src, 'a.txt'), 'w') as f:
        f.write('hello\n')
    return src, dst


def same(src, dst):
    a = os.path.join(src, 'big.bin')
    b = os.path.join(dst, 'big.bin')
    if not (os.path.exists(a) and os.path.exists(b)):
        return False
    with open(a, 'rb') as fa, open(b, 'rb') as fb:
        return fa.read() == fb.read()


def kill(*procs):
    for p in procs:
        try:
            if p and p.poll() is None:
                p.kill()
        except Exception:
            pass


def write_all(fd, data):
    if hasattr(fd, 'write'):
        fd.write(data)
        fd.flush()
    else:
        os.write(fd, data)


def read_some(fd, n):
    if hasattr(fd, 'fileno'):
        return os.read(fd.fileno(), n)
    return os.read(fd, n)


def run_pipe(src, dst, opts, timeout):
    """对照组：两个 rsync --server 走干净的双向管道（无 pty）"""
    a_r, a_w = os.pipe()   # 远端 stdout -> 本地 stdin
    b_r, b_w = os.pipe()   # 本地 stdout -> 远端 stdin

    remote = subprocess.Popen(
        ['rsync', '--server', '--sender', opts, '.', src + '/'],
        stdin=b_r, stdout=a_w, stderr=subprocess.PIPE)
    os.close(a_w)
    os.close(b_r)
    local = subprocess.Popen(
        ['rsync', '--server', opts, '.', dst + '/'],
        stdin=a_r, stdout=b_w, stderr=subprocess.PIPE)
    os.close(a_r)
    os.close(b_w)

    t0 = time.time()
    try:
        rc = local.wait(timeout=timeout)
        remote.wait(timeout=5)
    except subprocess.TimeoutExpired:
        kill(local, remote)
        return {'rc': 'TIMEOUT', 'secs': round(time.time() - t0, 1), 'note': '卡死（管道也卡？那问题不在 pty）'}
    secs = round(time.time() - t0, 1)
    err = (local.stderr.read() or b'').decode('utf-8', 'replace').strip()
    return {'rc': rc, 'secs': secs, 'note': err.splitlines()[-1][:90] if err else ''}


def run_pty(src, dst, opts, timeout):
    """被测：远端那一侧跑在 raw pty 上（模拟堡垒机给的目标机 shell，无 shell 层）"""
    master, slave = pty.openpty()
    tty.setraw(slave)

    remote = subprocess.Popen(
        ['rsync', '--server', '--sender', opts, '.', src + '/'],
        stdin=slave, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)
    local = subprocess.Popen(
        ['rsync', '--server', opts, '.', dst + '/'],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    stats = {'r2l': 0, 'l2r': 0}
    stop = threading.Event()

    def pump(src_fd, dst_fd, key):
        # pty 的 master 必须一直被抽干，否则 pty 缓冲满了会假死
        while not stop.is_set():
            try:
                r, _, _ = select.select([src_fd], [], [], 0.2)
            except (OSError, ValueError):
                break
            if not r:
                continue
            try:
                d = read_some(src_fd, 4096)
            except OSError:
                break
            if not d:
                break
            stats[key] += len(d)
            try:
                write_all(dst_fd, d)
            except (BrokenPipeError, OSError):
                break

    t1 = threading.Thread(target=pump, args=(master, local.stdin, 'r2l'), daemon=True)
    t2 = threading.Thread(target=pump, args=(local.stdout, master, 'l2r'), daemon=True)
    t1.start()
    t2.start()

    t0 = time.time()
    try:
        rc = local.wait(timeout=timeout)
        remote.wait(timeout=3)
    except subprocess.TimeoutExpired:
        kill(local, remote)
        stop.set()
        return {'rc': 'TIMEOUT', 'secs': round(time.time() - t0, 1),
                'note': '卡死（假设 H1 成立）', 'bytes': (stats['r2l'], stats['l2r'])}
    secs = round(time.time() - t0, 1)
    stop.set()
    err = (local.stderr.read() or b'').decode('utf-8', 'replace').strip()
    return {'rc': rc, 'secs': secs, 'note': err.splitlines()[-1][:90] if err else '',
            'bytes': (stats['r2l'], stats['l2r'])}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--timeout', type=float, default=20.0)
    args = ap.parse_args()

    ver = subprocess.run(['rsync', '--version'], capture_output=True, text=True)
    head = (ver.stdout or '').splitlines()[:2]
    print('ENV rsync=' + ' | '.join(h.strip() for h in head), flush=True)
    print('ENV timeout=%ss' % args.timeout, flush=True)
    print('', flush=True)
    print('%-14s %-8s %-9s %-6s %-12s %s' % ('variants', 'transport', 'result', 'secs', 'bytes r2l/l2r', 'note'), flush=True)
    print('-' * 100, flush=True)

    rows = []
    for label, opts in VARIANTS:
        for transport in ('pipe', 'pty'):
            tag = '%s_%s' % (transport, label.split()[0])
            src, dst = prepare(tag)
            fn = run_pipe if transport == 'pipe' else run_pty
            r = fn(src, dst, opts, args.timeout)
            r['match'] = same(src, dst)
            rows.append((label, transport, r))
            b = r.get('bytes')
            print('%-14s %-8s %-9s %-6s %-12s %s' % (
                label, transport, str(r['rc']), r['secs'],
                ('%d/%d' % b) if b else '-', r['note']), flush=True)

    print('-' * 100, flush=True)
    print('MATCH: ' + '  '.join('%s/%s=%s' % (l.split()[0], t, r['match']) for l, t, r in rows), flush=True)
    print('DONE', flush=True)


if __name__ == '__main__':
    main()
