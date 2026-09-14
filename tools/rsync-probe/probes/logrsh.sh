#!/bin/sh
# rsync -e 用的记录型 rsh：丢弃 host，把真实 --server 命令写进 /tmp/cmd.txt
shift
echo "$@" > /tmp/cmd.txt
