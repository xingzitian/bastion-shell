#!/usr/bin/env python3
"""Stage 1b: dump first bytes to find where the raw-pty stream corrupts."""
import os
import pty
import select
import shutil
import subprocess
import threading
import tty

SRC = '/tmp/rs-src'
DST = '/tmp/rs-dst'


def main():
    print('START', flush=True)
    shutil.rmtree(SRC, ignore_errors=True)
    shutil.rmtree(DST, ignore_errors=True)
    os.makedirs(SRC)
    os.makedirs(DST)
    open(os.path.join(SRC, 'big.bin'), 'wb').write(os.urandom(1024 * 1024))
    open(os.path.join(SRC, 'a.txt'), 'w').write('hello\n')

    master, slave = pty.openpty()
    tty.setraw(slave)

    remote = subprocess.Popen(
        ['rsync', '--server', '--sender', '-logDtpre.iLsfxC', '.', '/tmp/rs-src/'],
        stdin=slave, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)

    local = subprocess.Popen(
        ['rsync', '--server', '-logDtpre.iLsfxC', '.', '/tmp/rs-dst/'],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    r2l_bytes = bytearray()
    l2r_bytes = bytearray()

    def pump(src, dst, key, sink):
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
            sink.extend(d)
            if hasattr(dst, 'write'):
                dst.write(d)
                dst.flush()
            else:
                os.write(dst, d)

    t1 = threading.Thread(target=pump, args=(master, local.stdin, 'r2l', r2l_bytes), daemon=True)
    t2 = threading.Thread(target=pump, args=(local.stdout, master, 'l2r', l2r_bytes), daemon=True)
    t1.start()
    t2.start()

    try:
        rc = local.wait(timeout=10)
    except subprocess.TimeoutExpired:
        local.kill()
        rc = 'TIMEOUT'
    t1.join(timeout=2)
    t2.join(timeout=2)

    print('rc=', rc, flush=True)
    print('remote_to_local_len=', len(r2l_bytes), flush=True)
    print('local_to_remote_len=', len(l2r_bytes), flush=True)
    print('remote_to_local_hex=', r2l_bytes[:160].hex(), flush=True)
    print('local_to_remote_hex=', l2r_bytes[:160].hex(), flush=True)
    print('local_stderr=', local.stderr.read().decode('utf-8', 'replace'), flush=True)
    print('DONE', flush=True)


if __name__ == '__main__':
    main()
