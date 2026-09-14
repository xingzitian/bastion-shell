#!/usr/bin/env python3
"""bisect_shim.py —— rsync 的 `-e` 用 shim（rsh 角色）。

rsync 会这样调它：`python3 bisect_shim.py <host> rsync --server ... -opts . <dst>/`
职责：把远端命令发给 bisect_server，然后把这个进程的 stdio 与 unix socket 双向桥起来。

socket 路径从环境变量 BRIDGE_SOCK 取（由 bisect_server 注入），这样多次运行不会撞车。
"""
import os
import select
import socket
import sys
import threading

SOCK = os.environ.get('BRIDGE_SOCK', '/tmp/bisect.sock')
DEBUG = os.environ.get('BRIDGE_DEBUG') == '1'

# 对面（远端）结束了吗。这个标志是「客户端不退出」那个坑的解药，见 sock_to_stdout 里的注释。
remote_done = False


def main():
    global remote_done
    if len(sys.argv) < 3:
        sys.stderr.write('bisect_shim: 参数不足\n')
        sys.exit(2)
    remote_cmd = ' '.join(sys.argv[2:])
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        s.connect(SOCK)
    except OSError as e:
        sys.stderr.write('bisect_shim: 连不上桥 %s: %s\n' % (SOCK, e))
        sys.exit(1)
    s.sendall((remote_cmd + '\n').encode())
    if DEBUG:
        sys.stderr.write('[shim] 发出远端命令: %s\n' % remote_cmd)
        sys.stderr.flush()

    def sock_to_stdout():
        global remote_done
        while True:
            try:
                d = s.recv(65536)
            except OSError:
                break
            if not d:
                break
            try:
                sys.stdout.buffer.write(d)
                sys.stdout.buffer.flush()
            except (BrokenPipeError, OSError):
                break
        # ⚠️ 关键：对面结束了 → **我们也得结束**（真实 ssh 就是这个行为）。
        #
        # 这里踩过一个坑，实测快照如下（`--transport pipe` 超时那一刻）：
        #   SNAP[remote] 已退出 rc=0
        #   SNAP[client] S (sleeping) wchan=hrtimer_nanosleep   ← 客户端在等它的 rsh 子进程
        #     CHILD  python3 bisect_shim.py ... wchan=poll_schedule_timeout  ← shim 在等 stdin
        # 也就是说：rsync 等 shim 退出，shim 等 rsync 关 stdin —— 双方互等，永远不退。
        # 只把 stdout 关掉（让 rsync 读到 EOF）不够，进程本身也必须走。
        remote_done = True
        try:
            sys.stdout.buffer.flush()
        except Exception:
            pass
        try:
            os.close(1)
        except OSError:
            pass

    t = threading.Thread(target=sock_to_stdout, daemon=True)
    t.start()

    while True:
        if remote_done:                      # 对面结束了，别再等 rsync 关 stdin
            break
        try:
            r, _, _ = select.select([sys.stdin], [], [], 0.2)
        except (OSError, ValueError):
            break
        if r:
            try:
                d = os.read(0, 65536)
            except OSError:
                break
            if not d:
                break
            try:
                s.sendall(d)
            except OSError:
                break
    try:
        s.shutdown(socket.SHUT_WR)
    except OSError:
        pass
    t.join(timeout=3)


if __name__ == '__main__':
    main()
