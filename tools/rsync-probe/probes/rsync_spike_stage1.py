#!/usr/bin/env python3
"""Stage 1: can rsync's binary protocol survive a RAW pty at all? (no shell)"""
import os
import pty
import select
import shutil
import subprocess
import threading
import tty

SRC = '/tmp/rs-src'
DST = '/tmp/rs-dst'


def write_fd(dst, data):
    if hasattr(dst, 'write'):
        dst.write(data)
        dst.flush()
    else:
        os.write(dst, data)


def main():
    print('START stage1', flush=True)
    shutil.rmtree(SRC, ignore_errors=True)
    shutil.rmtree(DST, ignore_errors=True)
    os.makedirs(SRC)
    os.makedirs(DST)
    open(os.path.join(SRC, 'big.bin'), 'wb').write(os.urandom(1024 * 1024))
    open(os.path.join(SRC, 'a.txt'), 'w').write('hello\n')

    master, slave = pty.openpty()
    tty.setraw(slave)  # raw pty, no shell, no stty

    remote = subprocess.Popen(
        ['rsync', '--server', '--sender', '-logDtpre.iLsfxC', '.', '/tmp/rs-src/'],
        stdin=slave, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)

    local = subprocess.Popen(
        ['rsync', '--server', '-logDtpre.iLsfxC', '.', '/tmp/rs-dst/'],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    stats = {'r2l': 0, 'l2r': 0}

    def pump(src, dst, key):
        while True:
            r, _, _ = select.select([src], [], [], 0.5)
            if not r:
                if local.poll() is not None and remote.poll() is not None:
                    break
                continue
            try:
                d = os.read(src.fileno() if hasattr(src, 'fileno') else src, 4096)
            except OSError:
                break
            if not d:
                break
            stats[key] += len(d)
            try:
                write_fd(dst, d)
            except (BrokenPipeError, OSError):
                break

    t1 = threading.Thread(target=pump, args=(master, local.stdin, 'r2l'), daemon=True)
    t2 = threading.Thread(target=pump, args=(local.stdout, master, 'l2r'), daemon=True)
    t1.start()
    t2.start()

    try:
        rc = local.wait(timeout=15)
    except subprocess.TimeoutExpired:
        local.kill()
        rc = 'TIMEOUT'
    t1.join(timeout=3)
    t2.join(timeout=3)
    rerr = remote.poll()
    print(f'rc={rc} remote_poll={rerr} remote_to_local={stats["r2l"]} local_to_remote={stats["l2r"]}', flush=True)
    if rc == 0 and os.path.exists(os.path.join(DST, 'big.bin')):
        print('match=', open(os.path.join(SRC, 'big.bin'), 'rb').read() ==
              open(os.path.join(DST, 'big.bin'), 'rb').read(), flush=True)
    else:
        print('local_stderr=', local.stderr.read().decode('utf-8', 'replace'), flush=True)
    print('STAGE1_DONE', flush=True)


if __name__ == '__main__':
    main()
