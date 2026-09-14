#!/usr/bin/env bash
# 路线 A：真客户端 × 传输方式 × 是否剥 v 的四格二分。
# 在 WSL 里跑：bash run-bisect.sh
set -u
cd "$(dirname "$0")" || exit 1

printf 'rsync: %s\n' "$(rsync --version | head -1)"
printf 'python3: %s\n' "$(python3 --version 2>&1)"
echo

for transport in pipe pty; do
  for opts in passthrough nov; do
    echo "##################### transport=$transport opts=$opts #####################"
    timeout 70 python3 probes/bisect_server.py --transport "$transport" --opts "$opts" 2>&1 \
      | grep -v '^CLIENT_STDERR=\[shim\]'
    echo
  done
done
echo "ALL_DONE"
