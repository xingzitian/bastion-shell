#!/usr/bin/env python3
"""Spike: rsync --server over a raw PTY (simulating the menu-bastion target shell)."""
import os
import pty
import select
import shutil
import subprocess
import threading
import time

SRC = '/tmp/rs-src'
DST = '/tmp/rs-dst'


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
    with open(os.path.join(SRC, 'big.bin'), 'wb') as f:
        f.write(os.urandom(2 * 1024 * 1024))
    with open(os.path.join(SRC, 'a.txt'), 'w') as f:
        f.write('hello v1\n')

    master, slave = pty.openpty()
    sh = subprocess.Popen(['/bin/bash', '--norc', '--noprofile'],
                          stdin=slave, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)
    time.sleep(0.5)
    drain(master, 0.6)

    def run_cycle(remote_cmdline, local_argv, label):
        os.write(master, (remote_cmdline + "\r").encode())
        time.sleep(0.3)
        local = subprocess.Popen(local_argv, stdin=subprocess.PIPE,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)

        stats = {'to_local': 0, 'to_master': 0}
        greeting = threading.Event()
        raw_buf = b''  # raw pty output captured (for debugging)

        def pty_to_local():
            nonlocal raw_buf
            buf = b''
            while True:
                r, _, _ = select.select([master], [], [], 0.5)
                if not r:
                    if local.poll() is not None:
                        break
                    continue
                try:
                    data = os.read(master, 4096)
                except OSError:
                    break
                if not data:
                    break
                if not greeting.is_set():
                    buf += data
                    raw_buf = buf[-2000:]
                    idx = buf.find(b'@RSYNCD:')
                    if idx >= 0:
                        payload = buf[idx:]
                        buf = b''
                        greeting.set()
                        stats['to_local'] += len(payload)
                        try:
                            local.stdin.write(payload)
                            local.stdin.flush()
                        except (BrokenPipeError, OSError):
                            break
                else:
                    stats['to_local'] += len(data)
                    try:
                        local.stdin.write(data)
                        local.stdin.flush()
                    except (BrokenPipeError, OSError):
                        break
            try:
                local.stdin.close()
            except OSError:
                pass

        def local_to_pty():
            while True:
                r, _, _ = select.select([local.stdout], [], [], 0.5)
                if not r:
                    if local.poll() is not None:
                        break
                    continue
                try:
                    data = os.read(local.stdout.fileno(), 4096)
                except OSError:
                    break
                if not data:
                    break
                stats['to_master'] += len(data)
                try:
                    os.write(master, data)
                except OSError:
                    break

        t1 = threading.Thread(target=pty_to_local, daemon=True)
        t2 = threading.Thread(target=local_to_pty, daemon=True)
        t1.start()
        t2.start()
        try:
            rc = local.wait(timeout=20)
        except subprocess.TimeoutExpired:
            local.kill()
            rc = 'TIMEOUT'
        t1.join(timeout=3)
        t2.join(timeout=3)
        err = local.stderr.read().decode('utf-8', 'replace') if local.stderr else ''
        print(f'[{label}] rc={rc} greeting={greeting.is_set()} '
              f'remote_to_local={stats["to_local"]} local_to_remote={stats["to_master"]}', flush=True)
        if not greeting.is_set() or rc != 0:
            print(f'[{label}] local_stderr={err!r}', flush=True)
            print(f'[{label}] pty_raw_tail={raw_buf[-400:].decode("utf-8", "replace")!r}', flush=True)
        return rc, greeting.is_set(), stats

    sender = "rsync --server --sender -logDtpre.iLsfxC . /tmp/rs-src/"
    receiver = ['rsync', '--server', '-logDtpre.iLsfxC', '.', '/tmp/rs-dst/']

    print('=== cycle1 full ===', flush=True)
    run_cycle(sender, receiver, 'cycle1-full')
    same1 = open(os.path.join(SRC, 'big.bin'), 'rb').read() == open(os.path.join(DST, 'big.bin'), 'rb').read()
    print('cycle1 content_match=', same1, flush=True)

    time.sleep(1)
    with open(os.path.join(SRC, 'big.bin'), 'r+b') as f:
        f.seek(1024 * 1024)
        f.write(b'XYZW')

    print('=== cycle2 delta ===', flush=True)
    run_cycle(sender, receiver, 'cycle2-delta')
    same2 = open(os.path.join(SRC, 'big.bin'), 'rb').read() == open(os.path.join(DST, 'big.bin'), 'rb').read()
    print('cycle2 content_match=', same2, flush=True)

    sh.terminate()
    print('SPIKE_DONE', flush=True)


if __name__ == '__main__':
    main()
