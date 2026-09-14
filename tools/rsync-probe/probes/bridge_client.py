#!/usr/bin/env python3
"""bridge_client.py — rsync 的 -e 用桥（rsh 角色）：
rsync 会以 `python3 bridge_client.py <host> rsync --server ...` 方式调用。
职责：连接 bridge_server 的 unix socket，把远端命令发过去，然后桥接 stdio<->socket。
"""
import os
import select
import socket
import sys
import threading

SOCK = '/tmp/rsync_bridge.sock'


def main():
    if len(sys.argv) < 3:
        sys.exit(2)
    # sys.argv = [script, host, 'rsync', '--server', ...]
    remote_cmd = ' '.join(sys.argv[2:])

    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.connect(SOCK)
    sock.sendall((remote_cmd + '\n').encode())

    # socket -> rsync 的 stdout
    def sock_to_out():
        while True:
            try:
                d = sock.recv(65536)
            except OSError:
                break
            if not d:
                break
            try:
                sys.stdout.buffer.write(d)
                sys.stdout.buffer.flush()
            except (BrokenPipeError, OSError):
                break

    t = threading.Thread(target=sock_to_out, daemon=True)
    t.start()

    # rsync 的 stdin -> socket
    while True:
        r, _, _ = select.select([sys.stdin], [], [], 1.0)
        if r:
            try:
                d = os.read(0, 65536)
            except OSError:
                break
            if not d:
                break
            try:
                sock.sendall(d)
            except OSError:
                break
    try:
        sock.shutdown(socket.SHUT_WR)
    except OSError:
        pass
    t.join(timeout=3)


if __name__ == '__main__':
    main()
