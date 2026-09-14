#!/usr/bin/env bash
# 校验「注入到远端的那一行」的 shell 语法与 stderr 捕获路径。
#
# 为什么单独有个脚本：那一行是拼出来的 shell 代码，里面有引号、重定向、`||`、命令替换，
# 拼错了会在**远端**报语法错或重定向错（退出码 1），而现场只看得到"远端一个字节都不吐"。
# 这里用 </dev/null 让 rsync 立刻因 EOF 退出，从而把整行从头跑到尾，验证：
#   1) raw 标记能打出来；
#   2) stderr 落盘探测可用（/tmp 写不了会退到 $HOME）；
#   3) 完成标记带得出退出码；
#   4) 错误文件里确实有 rsync 的报错。
set -u
cd /tmp || exit 1
rm -f /tmp/bastion-rsync-err.txt "$HOME/.bastion-rsync-err.txt"

OUT=$(
  (
    stty raw -echo -iexten 2>/dev/null
    PS1=
    echo __BASTION_RSYNC_RAW__
    ERR=/tmp/bastion-rsync-err.txt
    : >"$ERR" 2>/dev/null || ERR="$HOME/.bastion-rsync-err.txt"
    rsync --server -ogDtpre.ifxCIvu . ./ 2>"$ERR"
    rc=$?
    stty sane 2>/dev/null
    echo "__BASTION_RSYNC_DONE_${rc}__"
  ) </dev/null 2>&1
)
echo "输出: $OUT"
echo "--- 错误文件（$HOME/.bastion-rsync-err.txt 或 /tmp/bastion-rsync-err.txt）---"
cat /tmp/bastion-rsync-err.txt 2>/dev/null || cat "$HOME/.bastion-rsync-err.txt" 2>/dev/null || echo '(没生成)'
echo "--- 判定 ---"
case "$OUT" in
  *__BASTION_RSYNC_RAW__*__BASTION_RSYNC_DONE_*__*) echo "OK: 整行语法正确，raw 标记与完成标记都在" ;;
  *) echo "FAIL: 整行没能正常跑完（语法/重定向有问题）" ;;
esac
