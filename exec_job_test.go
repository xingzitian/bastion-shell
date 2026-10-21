package main

// 桌面版的执行作业（与扩展侧同一套语义）：状态机 / 滚动日志 / 偏移切片 / pattern 等待 / 提示识别。

import (
	"strings"
	"testing"
	"time"
)

func newTestJob(t *testing.T) *execJob {
	t.Helper()
	j := createJob("s-test-"+t.Name(), 7, "make")
	t.Cleanup(func() {
		j.cancel()
		jobsMu.Lock()
		delete(jobs, j.id)
		jobsMu.Unlock()
	})
	return j
}

func TestExecJobStates(t *testing.T) {
	j := newTestJob(t)
	if j.view(7).Status != statusWorking {
		t.Fatalf("新建应该是 working，得到 %s", j.view(7).Status)
	}
	j.markInputRequired("Password: ", "在等密码")
	if v := j.view(7); v.Status != statusInputRequired || v.Prompt != "Password: " || v.Hint == "" {
		t.Fatalf("input_required 要带 prompt/hint：%+v", v)
	}
	j.resume()
	if j.view(7).Status != statusWorking {
		t.Fatal("resume 之后应该回到 working")
	}
	j.complete(0, true, true)
	if v := j.view(7); v.Status != statusCompleted || v.ExitCode != 0 || !v.MarkerSeen {
		t.Fatalf("completed 应带退出码/markerSeen：%+v", v)
	}
}

func TestExecJobNonZeroIsFailed(t *testing.T) {
	j := newTestJob(t)
	j.complete(2, true, true)
	v := j.view(7)
	if v.Status != statusFailed || v.Reason != "exit_nonzero" {
		t.Fatalf("非 0 退出码应记 failed/exit_nonzero：%+v", v)
	}
}

func TestExecJobOutputOffsetsAndDrop(t *testing.T) {
	j := newTestJob(t)
	j.appendOutput([]byte("line-1\n"))
	j.appendOutput([]byte("line-2\n"))
	if got := j.lastLine; got != "line-2" {
		t.Fatalf("lastLine 应该是最后一行，得到 %q", got)
	}
	chunk, next, _ := j.sliceFrom(0, 1024)
	if !strings.Contains(chunk, "line-1") || !strings.Contains(chunk, "line-2") {
		t.Fatalf("从头读应该拿到两行：%q", chunk)
	}
	if next != j.endOffset() {
		t.Fatalf("nextOffset 应该是末尾 %d，得到 %d", j.endOffset(), next)
	}
	// 分页读：拼回来等于原文
	j.appendOutput([]byte(strings.Repeat("x", 300)))
	var all strings.Builder
	off := int64(0)
	for i := 0; i < 50; i++ {
		c, nx, _ := j.sliceFrom(off, 64)
		if c == "" {
			break
		}
		all.WriteString(c)
		off = nx
	}
	if int64(all.Len()) != j.endOffset() {
		t.Fatalf("分页拼回来应该 %d 字节，得到 %d", j.endOffset(), all.Len())
	}
}

func TestExecJobWaitPatternMatchesImmediately(t *testing.T) {
	j := newTestJob(t)
	j.appendOutput([]byte("阶段 4/9 完成\n阶段 5/9 完成\n"))
	start := time.Now()
	v := j.waitFor("阶段 5/9 完成", 5000, 0, func() int64 { return 9 })
	if time.Since(start) > time.Second {
		t.Fatal("已经命中了就不该等满超时")
	}
	if v.Status != statusWorking {
		t.Fatalf("命中 pattern 不代表作业结束，状态应仍是 working：%s", v.Status)
	}
	if line := j.matchedLine("阶段 5/9 完成", 0); line == "" {
		t.Fatal("matchedLine 应该给出证据")
	}
}

func TestExecJobWaitReturnsOnCompletion(t *testing.T) {
	j := newTestJob(t)
	go func() {
		time.Sleep(30 * time.Millisecond)
		j.complete(0, true, true)
	}()
	v := j.waitFor("", 5000, 0, func() int64 { return 8 })
	if v.Status != statusCompleted {
		t.Fatalf("完成时应立刻返回 completed，得到 %s", v.Status)
	}
}

func TestExecJobWaitTimesOutAsWorking(t *testing.T) {
	j := newTestJob(t)
	start := time.Now()
	v := j.waitFor("永远不会出现", 60, 0, func() int64 { return 7 })
	if v.Status != statusWorking {
		t.Fatalf("超时应该仍是 working（不是 failed）：%s", v.Status)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("应该真的等了一会儿")
	}
}

func TestExecJobBusyPerSession(t *testing.T) {
	j := newTestJob(t)
	if got := activeJobOf(j.sessionID); got == nil || got.id != j.id {
		t.Fatalf("应该能查到这条会话上的作业：%v", got)
	}
	j.complete(0, true, true)
	if got := activeJobOf(j.sessionID); got != nil {
		t.Fatalf("作业结束后不该再占用会话：%v", got.id)
	}
}

func TestExecJobCancelAndSessionLost(t *testing.T) {
	j := newTestJob(t)
	j.cancel()
	if v := j.view(7); v.Status != statusCancelled {
		t.Fatalf("取消后应该是 cancelled：%+v", v)
	}
	// 终态之后不再改变
	j.fail("session_lost")
	if v := j.view(7); v.Status != statusCancelled {
		t.Fatalf("终态之后不该被改：%+v", v)
	}

	j2 := createJob("s-lost-"+t.Name(), 0, "sleep 300")
	j2.fail("session_lost")
	if v := j2.view(0); v.Status != statusFailed || v.Reason != "session_lost" {
		t.Fatalf("会话丢了应记 failed/session_lost：%+v", v)
	}
	jobsMu.Lock()
	delete(jobs, j2.id)
	jobsMu.Unlock()
}

func TestClampWaitMs(t *testing.T) {
	if got := clampWaitMs(0, 15000); got != 15000 {
		t.Fatalf("0 应该用默认值，得到 %d", got)
	}
	if got := clampWaitMs(20000, 15000); got != 20000 {
		t.Fatalf("预算内应原样返回，得到 %d", got)
	}
	if got := clampWaitMs(10*60*1000, 15000); got != int64(waitBudget/time.Millisecond) {
		t.Fatalf("超预算应夹到 %d，得到 %d", waitBudget/time.Millisecond, got)
	}
}

func TestDetectInputPrompt(t *testing.T) {
	cases := []string{
		"[sudo] password for deploy: ",
		"Password: ",
		"Verification code: ",
		"Are you sure? (yes/no) ",
		"Enter passphrase for key /root/.ssh/id_ed25519: ",
		"请选择目标机 [1-9]: ",
	}
	for _, c := range cases {
		if _, _, ok := detectInputPrompt(c); !ok {
			t.Fatalf("%q 应该识别为需要输入", c)
		}
	}
	if _, _, ok := detectInputPrompt("build ok\n"); ok {
		t.Fatal("普通输出不该被识别成提示")
	}
}
