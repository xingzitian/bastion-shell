#!/usr/bin/env bash
# 路线 A 第三轮：确认「客户端不退出」已经修好。
# 上一轮的关键对照（--transport local，没有 shim/socket）是 rc=0 —— 所以问题在我们那层。
set -u
cd "$(dirname "$0")" || exit 1

printf 'rsync: %s\n\n' "$(rsync --version | head -1)"

run() {
  echo "##################### $* #####################"
  timeout 60 python3 probes/bisect_server.py "$@" --timeout 15 2>&1 \
    | grep -E 'ENV|RESULT|MATCH|PREFIX_DROPPED|CLIENT_STDERR|mismatch|SNAP|超时|已退出' \
    | grep -v '^CLIENT_STDERR=\[shim\]'
  echo
}

run --transport local
run --transport pipe
run --transport pipe --opts nov
run --transport pty --shell exec
run --transport pty --shell bash --filter-prefix
echo "ALL_DONE"
