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
	bakInfo, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("expect backup %s created, err=%v", backup, err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "day10 logs" {
		t.Fatalf("backup content should be preserved, data=%q err=%v", data, err)
	}
	// 不得 touch 成归档时刻：读取侧以后缀与 mtime 的 48h 容差判定内容时间，
	// 长时间停机后改 mtime 会让旧日志排序错位。
	if delta := bakInfo.ModTime().Sub(yesterday); delta > time.Second || delta < -time.Second {
		t.Fatalf("backup mtime must keep the content time %v, got %v", yesterday, bakInfo.ModTime())
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

	backup2 := filepath.Join(dir, "som.log.20261110_2")
	if _, err := os.Stat(backup2); err != nil {
		t.Fatalf("conflicting backup should get _2 suffix, err=%v", err)
	}
	if data, _ := os.ReadFile(backup2); string(data) != "new content" {
		t.Fatalf("new backup content = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "som.log.20261110")); string(data) != "old backup" {
		t.Fatalf("existing backup was overwritten: %q", data)
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

// 备份名连续冲突：占位序号继续递增到 _3，且不会覆盖任何历史文件。
func TestWriter_BootstrapMissedTimeRotate_BackupNameConflictThird(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	yesterday := time.Date(2026, 11, 10, 15, 30, 0, 0, time.Local)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	writeAndTouch(t, logfile, "new content", yesterday)
	writeAndTouch(t, filepath.Join(dir, "som.log.20261110"), "backup 1", yesterday)
	writeAndTouch(t, filepath.Join(dir, "som.log.20261110_2"), "backup 2", yesterday)

	w := newBootstrapWriter(t, dir, "som.log", EveryDay, now)
	defer w.Close()

	backup3 := filepath.Join(dir, "som.log.20261110_3")
	if data, err := os.ReadFile(backup3); err != nil || string(data) != "new content" {
		t.Fatalf("expect new backup at _3 with original content, data=%q err=%v", data, err)
	}
	for name, want := range map[string]string{
		"som.log.20261110":   "backup 1",
		"som.log.20261110_2": "backup 2",
	} {
		if data, _ := os.ReadFile(filepath.Join(dir, name)); string(data) != want {
			t.Fatalf("historical backup %s was overwritten: %q", name, data)
		}
	}
}

// ModeCreate 的活跃文件名自带周期后缀，不应触发补偿转存。
func TestWriter_Bootstrap_ModeCreateUnaffected(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	yesterday := time.Date(2026, 11, 10, 15, 30, 0, 0, time.Local)
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	writeAndTouch(t, logfile, "day10 logs", yesterday)

	cfg := EmptyConfigWith(
		WithFilepath(logfile),
		func(c *Config) {
			c.RotateTime = EveryDay
			c.RotateMode = ModeCreate
		},
		fixedClock(now),
	)
	w, err := NewWriter(cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(filepath.Join(dir, "som.log.20261111")); err != nil {
		t.Fatalf("ModeCreate should open a suffixed active log, err=%v", err)
	}
	if data, _ := os.ReadFile(logfile); string(data) != "day10 logs" {
		t.Fatalf("existing file must not be touched: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "som.log.20261110")); !os.IsNotExist(err) {
		t.Fatalf("ModeCreate must not produce a bootstrap backup, err=%v", err)
	}
}

// 秒级轮转：FirstCheckTime 返回 now+interval，周期起点恰为启动时刻，
// 补偿会退化成"每次重启都切割日志"，必须直接跳过。
func TestWriter_Bootstrap_SecondLevelSkipped(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "sec.log")
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	writeAndTouch(t, logfile, "recent logs", now.Add(-time.Second))

	w := newBootstrapWriter(t, dir, "sec.log", EverySecond, now)
	defer w.Close()

	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("second-level restart must not create a backup, got %v", entries)
	}
	if data, _ := os.ReadFile(logfile); string(data) != "recent logs" {
		t.Fatalf("active log must keep its content, got %q", data)
	}
}

// 备份路径用 O_EXCL 原子占位：已有备份不被覆盖，重复占位得到递增序号。
func TestWriter_ReserveBackupPath_NoOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "som.log.20261110")
	if err := os.WriteFile(target, []byte("old backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local)
	w := newBootstrapWriter(t, dir, "probe.log", EveryDay, now)
	defer w.Close()

	first, err := w.reserveBackupPath(target)
	if err != nil {
		t.Fatalf("reserveBackupPath: %v", err)
	}
	if first != target+"_2" {
		t.Fatalf("first free name should be _2, got %s", first)
	}
	if fi, err := os.Stat(first); err != nil || fi.Size() != 0 {
		t.Fatalf("placeholder should be an empty file, fi=%v err=%v", fi, err)
	}
	second, err := w.reserveBackupPath(target)
	if err != nil {
		t.Fatalf("reserveBackupPath: %v", err)
	}
	if second != target+"_3" {
		t.Fatalf("second free name should be _3, got %s", second)
	}
	if data, _ := os.ReadFile(target); string(data) != "old backup" {
		t.Fatalf("existing backup was overwritten: %q", data)
	}
}

// 长时间停机（跨过读取侧 48h 容差）时，备份 mtime 必须保持内容时间：
// 否则读取侧会回退用 mtime 判定内容日期，把 11-01 的日志排到归档日。
func TestWriter_Bootstrap_LongOutageKeepsContentMtime(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "som.log")
	contentDay := time.Date(2026, 11, 1, 15, 30, 0, 0, time.Local)
	now := contentDay.AddDate(0, 0, 10) // 停机 10 天后才启动
	writeAndTouch(t, logfile, "old logs", contentDay)

	w := newBootstrapWriter(t, dir, "som.log", EveryDay, now)
	defer w.Close()

	backup := filepath.Join(dir, "som.log.20261101")
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("expect backup %s created, err=%v", backup, err)
	}
	if delta := info.ModTime().Sub(contentDay); delta > time.Second || delta < -time.Second {
		t.Fatalf("backup mtime must stay at content time %v, got %v", contentDay, info.ModTime())
	}
	if got := info.ModTime().Format("20060102"); got != "20261101" {
		t.Fatalf("backup mtime must match the date suffix 20261101, got %s", got)
	}
}
