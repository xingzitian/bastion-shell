#!/usr/bin/env bash
# 在 WSL 里跑「不需要堡垒机」的那部分 rsync 探针，并把**环境**一起记下来。
#
# 为什么要记环境：这些探针的结论是**跟环境绑定**的（rsync 版本、协议号、pty 行为）。
# 当年「本机 WSL 已装 rsync」的那套环境现在已经变了（默认发行版里连 rsync 都没有），
# 所以每次都把环境写下来，才能判断「这次结论和上次是不是同一件事」。
#
# 用法（在 Windows 侧）：
#   wsl -d Ubuntu       -- bash <这个脚本的 /mnt/c 路径>
#   wsl -d AlmaLinux-10 -- bash <这个脚本的 /mnt/c 路径>
set -u
cd "$(dirname "$0")" || exit 1

echo "==================== 环境 ===================="
printf 'distro   : %s\n' "$(grep PRETTY_NAME /etc/os-release 2>/dev/null | cut -d= -f2- | tr -d '"')"
printf 'kernel   : %s\n' "$(uname -sr)"
printf 'WSL      : %s\n' "$(grep -qi microsoft /proc/version && echo yes || echo no)"
if command -v rsync >/dev/null 2>&1; then
  rsync --version | head -2 | sed 's/^/rsync    : /'
else
  echo "rsync    : MISSING —— 这一台跑不了探针（apt-get install rsync / dnf install rsync）"
  echo "STOP: 缺 rsync，直接退出（不要在这里假装跑过）"
  exit 3
fi
printf 'python3  : %s\n' "$(python3 --version 2>&1)"
printf 'date     : %s\n' "$(date -Is)"
echo

echo "==================== 探针 1/4：pty 注入机制冒烟（先证明机制没问题） ===================="
python3 probes/pty_inject_smoke.py; echo "rc=$?"
echo

echo "==================== 探针 2/4：干净管道对照（带 v） ===================="
python3 probes/rsync_pipe_control.py; echo "rc=$?"
echo

echo "==================== 探针 3/4：raw pty（剥掉 v） ===================="
python3 probes/rsync_spike_stage1.py; echo "rc=$?"
echo

echo "==================== 探针 4/4：v 标志 × 传输方式 矩阵 ===================="
python3 probes/vflag_matrix.py; echo "rc=$?"
echo
echo "ALL_DONE"
