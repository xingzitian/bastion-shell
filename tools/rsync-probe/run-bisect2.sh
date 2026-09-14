#!/usr/bin/env bash
# 路线 A 第二轮：把 pty 上的失败原因钉死。
#
#   1) pty + 交互式 bash（注入命令）           ← 复现「shell 噪声」
#   2) pty + 交互式 bash + 过滤前导噪声        ← 验证根因是不是"噪声"
#   3) pty + 非交互 exec（没有 readline）      ← 反证：没有 shell 噪声就该通
#
# 判读：2) 和 3) 只要有一格通，就证明问题在「协议开始前的终端噪声」，与 'v' 无关。
set -u
cd "$(dirname "$0")" || exit 1

printf 'rsync: %s\n\n' "$(rsync --version | head -1)"

run() {
  echo "##################### $* #####################"
  timeout 70 python3 probes/bisect_server.py "$@" 2>&1 | grep -v '^CLIENT_STDERR=\[shim\]'
  echo
}

run --transport pty --opts passthrough --shell bash
run --transport pty --opts passthrough --shell bash --filter-prefix
run --transport pty --opts passthrough --shell exec
run --transport pty --opts nov --shell exec
echo "ALL_DONE"
