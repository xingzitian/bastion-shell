#!/usr/bin/env python3
"""Control test: two rsync --server over a clean bidirectional pipe (NO pty)."""
import os
import shutil
import subprocess

SRC = '/tmp/rs-src'
DST = '/tmp/rs-dst'


def main():
    shutil.rmtree(SRC, ignore_errors=True)
    shutil.rmtree(DST, ignore_errors=True)
    os.makedirs(SRC)
    os.makedirs(DST)
    open(os.path.join(SRC, 'big.bin'), 'wb').write(os.urandom(1024 * 1024))
    open(os.path.join(SRC, 'a.txt'), 'w').write('hello\n')

    # full-duplex via two pipes (clean, no pty, no threads)
    a_r, a_w = os.pipe()  # remote.stdout -> a_w -> a_r -> local.stdin
    b_r, b_w = os.pipe()  # local.stdout -> b_w -> b_r -> remote.stdin

    remote = subprocess.Popen(
        ['rsync', '--server', '--sender', '-vvv', '-logDtpre.iLsfxCIvu', '.', '/tmp/rs-src/'],
        stdin=b_r, stdout=a_w, stderr=subprocess.PIPE)
    os.close(a_w)
    os.close(b_r)

    local = subprocess.Popen(
        ['rsync', '--server', '-vvv', '-logDtpre.iLsfxCIvu', '.', '/tmp/rs-dst/'],
        stdin=a_r, stdout=b_w, stderr=subprocess.PIPE)
    os.close(a_r)
    os.close(b_w)

    rc = local.wait(timeout=15)
    remote.wait(timeout=15)
    print('local_rc=', rc, flush=True)
    print('local_stderr=', local.stderr.read().decode('utf-8', 'replace'), flush=True)
    print('remote_stderr=', remote.stderr.read().decode('utf-8', 'replace'), flush=True)
    if os.path.exists(os.path.join(DST, 'big.bin')):
        print('match=', open(os.path.join(SRC, 'big.bin'), 'rb').read() ==
              open(os.path.join(DST, 'big.bin'), 'rb').read(), flush=True)
    else:
        print('dst big.bin missing', flush=True)
    print('DONE', flush=True)


if __name__ == '__main__':
    main()
