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

// fixedClock 返回固定时间的 Clocker，避免测试依赖真实时钟。
func fixedClock(t time.Time) ConfigFn {
	return func(c *Config) {
		c.TimeClock = ClockFn(func() time.Time { return t })
	}
}

func newBootstrapWriter(t *testing.T, dir, name string, rt RotateTime, now time.Time) *Writer {
	t.Helper()
	cfg := EmptyConfigWith(
		WithFilepath(filepath.Join(dir, name)),
		func(c *Config) { c.RotateTime = rt },
		fixedClock(now),
	)
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

func writeAndTouch(t *testing.T, path, content string, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// 跨天重启：昨天的活跃日志应在启动时被转存为带昨天日期的备份，
// 随后重新创建一个全新的活跃文件（对应 11-10 关机、11-11 启动的现场）。
func TestWriter_BootstrapMissedTimeRotate_CrossDay(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	yesterday := time.Date(2026, 11, 10, 15, 30, 0, 0, time.Local)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	writeAndTouch(t, logfile, "day10 logs", yesterday)

	w := newBootstrapWriter(t, dir, "som.log", EveryDay, now)
	defer w.Close()

	backup := filepath.Join(dir, "som.log.20261110")
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("expect backup %s created, err=%v", backup, err)
	}
	info, err := os.Stat(logfile)
	if err != nil {
		t.Fatalf("active log should be recreated, err=%v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("new active log should be empty, got size %d", info.Size())
	}
}

// 同一天内重启：文件仍属于当前周期，不得触发补偿转存。
func TestWriter_BootstrapMissedTimeRotate_SameDayNoRotate(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	writeAndTouch(t, logfile, "today logs", now.Add(-time.Hour))

	w := newBootstrapWriter(t, dir, "som.log", EveryDay, now)
	defer w.Close()

	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("same-day restart must not create backup, got %v", entries)
	}
}

// 备份名冲突：已存在同名备份时，新备份追加 _2 序号，不覆盖历史文件。
func TestWriter_BootstrapMissedTimeRotate_BackupNameConflict(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	yesterday := time.Date(2026, 11, 10, 15, 30, 0, 0, time.Local)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	writeAndTouch(t, logfile, "new content", yesterday)
	writeAndTouch(t, filepath.Join(dir, "som.log.20261110"), "old backup", yesterday)

	w := newBootstrapWriter(t, dir, "som.log", EveryDay, now)
	defer w.Close()

	if _, err := os.Stat(filepath.Join(dir, "som.log.20261110_2")); err != nil {
		t.Fatalf("conflicting backup should get _2 suffix, err=%v", err)
	}
}

// 按小时轮转同样需要补偿：上一小时的文件转存为小时后缀备份。
func TestWriter_BootstrapMissedTimeRotate_CrossHour(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "app.log")
	lastHour := time.Date(2026, 11, 11, 8, 10, 0, 0, time.Local)
	now := time.Date(2026, 11, 11, 9, 5, 0, 0, time.Local)
	writeAndTouch(t, logfile, "hour8 logs", lastHour)

	w := newBootstrapWriter(t, dir, "app.log", EveryHour, now)
	defer w.Close()

	// 小时桶后缀 layout 为 "20060102_1500"，分钟位是字面量 00，
	// 因此 08 点这一小时的备份固定为 _0800，与运行期 rotatingByTime 命名一致。
	backup := filepath.Join(dir, "app.log.20261111_0800")
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("expect hourly backup %s, err=%v", backup, err)
	}
}

// 重启后 written 必须继承已有文件大小，而不是从 0 重新计数，
// 否则尺寸轮转会延迟近一个 MaxSize 才触发。
func TestWriter_OpenFile_InheritsExistingSize(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	content := "0123456789" // 10 字节，当天文件不触发时间补偿
	writeAndTouch(t, logfile, content, now)

	w := newBootstrapWriter(t, dir, "som.log", EveryDay, now)
	defer w.Close()

	if w.written != uint64(len(content)) {
		t.Fatalf("written should inherit existing size %d, got %d", len(content), w.written)
	}
}

// 首次启动、活跃文件不存在：正常创建，不报错也不产生备份。
func TestWriter_Bootstrap_FirstStartNoFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)

	w := newBootstrapWriter(t, dir, "fresh.log", EveryDay, now)
	defer w.Close()

	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("first start should only create the active log, got %v", entries)
	}
}
