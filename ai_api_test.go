package main

import (
	"os"
	"strings"
	"testing"
)

// 工具实现层（ai_api.go）里不需要 SSH 的那部分：`bastion_habits` 的读写闭环。
//
// 为什么要单独测这一层：`editHabits`/`appendHabit` 有单测，但**工具层**（action 分发、
// 档案名归一、返回文案）是 AI 真正看到的那一面 —— 它错了，AI 就会"以为记下来了"。
// 这里用真实的共享目录（临时目录）+ 真实的文件读写，只把会话换成不需要的。

func TestHabitsToolReadWriteRoundTrip(t *testing.T) {
	dir := withSharedDir(t)
	mustWrite(t, dir, habitsFile, habitsSample)
	api := desktopSessionAPI{}

	// read：要能报出全局与档案级习惯，并说明文件在哪（两个版本共读）
	out, err := api.Habits("read", "生产", "", "")
	if err != nil {
		t.Fatalf("read 出错：%v", err)
	}
	for _, want := range []string{"提权习惯", "sudo", "不要动 nginx.conf", habitsFile, "读同一份"} {
		if !strings.Contains(out, want) {
			t.Errorf("read 结果缺少 %q：\n%s", want, out)
		}
	}

	// remember（全局）
	out, err = api.Habits("remember", "", "这台机器的日志在 /var/log/app", "")
	if err != nil {
		t.Fatalf("remember 出错：%v", err)
	}
	if !strings.Contains(out, "已记录习惯") {
		t.Fatalf("remember 应该明确说记下来了：%s", out)
	}
	// 重复记同一条：要说"没记下来"，不能让 AI 以为写了两次
	if out, _ := api.Habits("remember", "", "这台机器的日志在 /var/log/app", ""); !strings.Contains(out, "没记下来") {
		t.Fatalf("重复记录应该如实说没写：%s", out)
	}

	// setPrivilege（档案级，档案还不存在 → 要把嵌套结构补出来）
	out, err = api.Habits("setPrivilege", "新档案", "", "none")
	if err != nil {
		t.Fatalf("setPrivilege 出错：%v", err)
	}
	if !strings.Contains(out, "none") {
		t.Fatalf("setPrivilege 应该回显设成了什么：%s", out)
	}
	if h := readHabits(); resolvePrivilege(h, "新档案") != privNone {
		t.Fatalf("档案级提权没落盘：%+v", h.Profiles)
	}

	// 非法取值/未知 action：要给出可读的原因，而不是静默失败
	if out, _ := api.Habits("setPrivilege", "", "", "乱写"); !strings.Contains(out, "错误") {
		t.Fatalf("非法提权方式应该报错：%s", out)
	}
	if out, _ := api.Habits("乱写的动作", "", "", ""); !strings.Contains(out, "错误") {
		t.Fatalf("未知 action 应该报错：%s", out)
	}
	if out, _ := api.Habits("remember", "", "   ", ""); !strings.Contains(out, "错误") {
		t.Fatalf("空的 habit 应该报错：%s", out)
	}

	// 用户手写的注释必须还在（这是"两个实现共读一份"能不能成立的底线）
	raw, err := os.ReadFile(sharedFilePath(habitsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"// BastionShell 个人习惯", "// 全局提权方式"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("写回把注释冲掉了，缺少 %q：\n%s", want, raw)
		}
	}
}

// 提权习惯必须**真的影响行为**：记成免密 sudo 时，`sudo <命令>` 才敢加命令结束标记。
func TestHabitsPrivilegeDrivesExecMarkerSafety(t *testing.T) {
	dir := withSharedDir(t)
	api := desktopSessionAPI{}

	// 没记录 → ask → 不给 sudo 加哨兵（怕弹密码卡住）
	if isMarkerSafe("sudo systemctl restart nginx", resolvePrivilege(currentHabits(), "") == privSudo) {
		t.Fatal("ask 时不该给 sudo 命令加哨兵（万一弹密码就卡住了）")
	}
	if _, err := api.Habits("setPrivilege", "", "", "sudo"); err != nil {
		t.Fatalf("设置失败：%v", err)
	}
	if !isMarkerSafe("sudo systemctl restart nginx", resolvePrivilege(currentHabits(), "") == privSudo) {
		t.Fatal("记成免密 sudo 之后，sudo 命令应该能用哨兵判定（否则只能靠静止兜底）")
	}
	// 但 sudo -i 仍然不能加（那会换成交互式 shell）
	if isMarkerSafe("sudo -i", resolvePrivilege(currentHabits(), "") == privSudo) {
		t.Fatal("sudo -i 一律不加哨兵")
	}
	_ = dir
}
