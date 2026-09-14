#!/usr/bin/env python3
"""pty 注入冒烟测试：把「机制」和「rsync」分开。

为什么需要它
------------
`bridge_server.py` 把命令注入 pty 上那个 bash，然后靠 `__RSYNC_DONE_<rc>__` 标记收回退出码。
本地跑它时出现了「两个方向都是 0 字节、注入的命令根本没执行（连 stderr 重定向文件都没建）」，
这时**无法判断**是「注入机制在当前终端上就不成立」，还是「rsync 协议那一层的问题」。
本脚本只测机制：开 pty → 起 bash → 写一行命令 → 看它有没有回显/执行。

判读
----
  ECHO_SEEN=True  RUN_SEEN=True   → 注入机制没问题，问题在 rsync 或桥的其它环节
  ECHO_SEEN=False RUN_SEEN=False  → **注入机制在这台机器上就不成立**
                                     （多半是 tty 模式/换行翻译的差异：脚本发的是 \\r，
                                      ICRNL 关掉时它不会变成换行，bash 就一直等）
用法: python3 probes/pty_inject_smoke.py
"""

import os
import pty
import select
import subprocess
import time

MARK = 'HELLO_MARK_9f3a'


def drain(fd, dur):
    out = b''
    end = time.time() + dur
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.1)
        if not r:
            continue
        try:
            d = os.read(fd, 4096)
        except OSError:
            break
        if not d:
            break
        out += d
    return out


def main():
    master, slave = pty.openpty()
    bash = subprocess.Popen(['/bin/bash', '--norc', '--noprofile'],
                            stdin=slave, stdout=slave, stderr=slave, close_fds=True)
    os.close(slave)
    time.sleep(0.5)
    banner = drain(master, 0.5)
    print('BANNER=%r' % banner[:120], flush=True)

    # 和 bridge_server.py 一样：只发 \r（不发光 \n）
    os.write(master, ('echo %s\r' % MARK).encode())
    out_cr = drain(master, 1.5)
    print('CR_ECHO_SEEN=%s CR_RUN_SEEN=%s' % (MARK in out_cr.decode('utf-8', 'replace'),
                                              out_cr.decode('utf-8', 'replace').count(MARK) >= 2), flush=True)
    print('CR_OUT=%r' % out_cr[:200], flush=True)

    # 对照：发 \r\n
    os.write(master, ('echo %s_B\r\n' % MARK).encode())
    out_crlf = drain(master, 1.5)
    print('CRLF_ECHO_SEEN=%s CRLF_RUN_SEEN=%s' % (MARK + '_B' in out_crlf.decode('utf-8', 'replace'),
                                                  out_crlf.decode('utf-8', 'replace').count(MARK + '_B') >= 2), flush=True)
    print('CRLF_OUT=%r' % out_crlf[:200], flush=True)

    bash.kill()
    print('DONE', flush=True)


if __name__ == '__main__':
    main()
