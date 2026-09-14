#!/usr/bin/env bash
# 看 rsync 的 --info=progress2 输出长什么样（纯客户端选项，不会传给远端）。
# 用「本地真跑」：-e 指向一个把远端命令原样执行的脚本，于是真的会传一遍。
set -u
cd /tmp || exit 1
rm -rf rsprog && mkdir -p rsprog/src rsprog/dst
head -c 20000000 /dev/urandom > rsprog/src/big.bin

cat > rsprog/rsh.sh <<'EOF'
#!/bin/sh
shift
exec rsync "$@"
EOF
chmod +x rsprog/rsh.sh

cd rsprog || exit 1
echo '--- 客户端 stderr 里的进度行（\r 换成 \n，只留关键几行）---'
rsync -a --info=progress2 --stats --timeout=20 -e ./rsh.sh src/ host:dst/ 2>&1 |
  tr '\r' '\n' |
  grep -E '%|Number of regular files|Total transferred file size' |
  tail -8
echo '--- 远端命令（确认 --info 没被带过去）---'
cat /tmp/cmd.txt 2>/dev/null || echo '(这次 -e 不是记录型 rsh，没有 cmd.txt)'
