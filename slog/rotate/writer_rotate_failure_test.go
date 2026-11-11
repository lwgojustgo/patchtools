/*
 * Copyright (C) 2026 KQ GEO Technologies Co., Ltd.
 * All rights reserved.
 */

package rotate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newHourlyWriter 创建按小时轮转、时钟可推进的 Writer。
func newHourlyWriter(t *testing.T, dir string, now *time.Time) *Writer {
	t.Helper()
	cfg := EmptyConfigWith(
		WithFilepath(filepath.Join(dir, "app.log")),
		func(c *Config) {
			c.RotateTime = EveryHour
			c.TimeClock = ClockFn(func() time.Time { return *now })
		},
	)
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

// rename 失败（备份目标是已存在的目录）后必须恢复句柄，后续日志不能停写。
func TestWriter_RotatingFile_RenameFailureKeepsWritable(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "app.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	w, err := NewWriter(EmptyConfigWith(WithFilepath(logfile), fixedClock(now)))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	blocker := filepath.Join(dir, "blocker")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.rotatingFile(blocker, true); err == nil {
		t.Fatal("rename to an existing directory should fail")
	}
	if w.file == nil {
		t.Fatal("file handle must be restored after a failed rename")
	}
	if _, err := w.WriteString("after-failure"); err != nil {
		t.Fatalf("write after failed rename: %v", err)
	}
	if data, err := os.ReadFile(logfile); err != nil || string(data) != "after-failure" {
		t.Fatalf("log content = %q, err=%v", data, err)
	}
}

// 句柄失效后由 fileHandle 自愈重开；重开也失败时返回错误而不是 panic/静默停写。
func TestWriter_FileHandle_SelfHealAndError(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "app.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	w, err := NewWriter(EmptyConfigWith(WithFilepath(logfile), fixedClock(now)))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if err := w.file.Close(); err != nil {
		t.Fatal(err)
	}
	w.file = nil
	if _, err := w.WriteString("self-heal"); err != nil {
		t.Fatalf("fileHandle should reopen the log: %v", err)
	}
	if data, err := os.ReadFile(logfile); err != nil || string(data) != "self-heal" {
		t.Fatalf("log content = %q, err=%v", data, err)
	}

	if err := w.file.Close(); err != nil {
		t.Fatal(err)
	}
	w.file = nil
	blocker := filepath.Join(dir, "blocker")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	w.path = blocker // 目录无法以 O_WRONLY 打开，重开必然失败
	if _, err := w.WriteString("x"); err == nil {
		t.Fatal("expected a reopen error, got nil")
	}
	if err := w.Sync(); err == nil {
		t.Fatal("expected a reopen error from Sync, got nil")
	}
	if err := w.Flush(); err == nil {
		t.Fatal("expected a reopen error from Flush, got nil")
	}
}

// 一次 rename 失败不能跳过整个周期：保持时间边界不变，占用解除后在同一周期内重试成功。
func TestWriter_TimeRotate_FailureDoesNotSkipPeriod(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "app.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	w := newHourlyWriter(t, dir, &now)
	defer w.Close()

	// 运行期按小时轮转生成的备份名（9:59:59.5 边界格式化为 _0900）
	backup := filepath.Join(dir, "app.log.20261111_0900")
	if err := os.Mkdir(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	boundary := w.nextRotatingAt

	now = time.Date(2026, 11, 11, 10, 0, 1, 0, time.Local)
	if _, err := w.WriteString("hello"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !w.nextRotatingAt.Equal(boundary) {
		t.Fatalf("failed rotate must not advance the boundary: %v -> %v", boundary, w.nextRotatingAt)
	}
	if w.rotateFailures != 1 {
		t.Fatalf("expected one recorded failure, got %d", w.rotateFailures)
	}
	if w.file == nil {
		t.Fatal("handle must stay usable after a failed rotate")
	}

	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 11, 10, 30, 0, 0, time.Local)
	if _, err := w.WriteString("world"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "helloworld" {
		t.Fatalf("rotation was not retried in the same period, backup=%q err=%v", data, err)
	}
	if !w.nextRotatingAt.After(now) {
		t.Fatalf("boundary should move past now after a successful rotate: %v", w.nextRotatingAt)
	}
	if fi, err := os.Stat(logfile); err != nil || fi.Size() != 0 {
		t.Fatalf("new active log should be empty, fi=%v err=%v", fi, err)
	}
}

// 轮转失败后按指数退避，避免每条日志都执行 sync + rename + reopen。
func TestWriter_RotateFailure_Backoff(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	w := newHourlyWriter(t, dir, &now)
	defer w.Close()

	backup := filepath.Join(dir, "app.log.20261111_0900")
	if err := os.Mkdir(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 11, 10, 0, 1, 0, time.Local)
	if _, err := w.WriteString("a"); err != nil {
		t.Fatal(err)
	}
	// 时钟固定不变，退避期内连续写入不应重复尝试轮转
	for i := 0; i < 10; i++ {
		if _, err := w.WriteString("b"); err != nil {
			t.Fatal(err)
		}
	}
	if w.rotateFailures != 1 {
		t.Fatalf("backoff should suppress retries, got %d failures", w.rotateFailures)
	}

	now = time.Date(2026, 11, 11, 10, 0, 3, 0, time.Local)
	if _, err := w.WriteString("c"); err != nil {
		t.Fatal(err)
	}
	if w.rotateFailures != 2 {
		t.Fatalf("retry expected after backoff, got %d failures", w.rotateFailures)
	}
	// 手动 Rotate 应绕过退避
	if err := w.Rotate(); err == nil {
		t.Fatal("rename is still blocked, expected an error")
	}
	if w.rotateFailures != 3 {
		t.Fatalf("manual Rotate should bypass backoff, got %d failures", w.rotateFailures)
	}
}

// 重启后 written 继承已有大小，第一次写入即触发尺寸轮转（端到端）。
func TestWriter_SizeRotate_AfterRestart(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	content := "0123456789" // 10 字节，> MaxSize
	writeAndTouch(t, logfile, content, now)

	backup := filepath.Join(dir, "som.log.bak")
	cfg := EmptyConfigWith(
		WithFilepath(logfile),
		func(c *Config) {
			c.MaxSize = 5
			c.RotateTime = EveryDay
			c.RenameFunc = func(string, uint) string { return backup }
		},
		fixedClock(now),
	)
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if w.written != uint64(len(content)) {
		t.Fatalf("written should inherit existing size, got %d", w.written)
	}
	if _, err := w.WriteString("x"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != content+"x" {
		t.Fatalf("backup content = %q, err=%v", data, err)
	}
	if fi, err := os.Stat(logfile); err != nil || fi.Size() != 0 {
		t.Fatalf("new active log should be empty, fi=%v err=%v", fi, err)
	}
}
