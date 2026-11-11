/*
 * Copyright (C) 2019 KQ GEO Technologies Co., Ltd.
 * All rights reserved.
 */

package rotate

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gookit/goutil/errorx"
	"github.com/gookit/goutil/fsutil"
	"github.com/gookit/goutil/mathutil"
	"github.com/gookit/goutil/strutil"
	"github.com/gookit/goutil/x/stdio"
)

// Writer a flush, close, writer and support rotate file.
//
// refer https://github.com/flike/golog/blob/master/filehandler.go
type Writer struct {
	// writer instance id, use for debug
	id string
	mu sync.RWMutex
	// config of the writer
	cfg *Config

	// current opened logfile
	file *os.File
	// current opened file path. NOTE it maybe not equals Config.Filepath
	path string
	// The original file dir path for the Config.Filepath
	fileDir string
	// The original name and ext information
	fileName, onlyName, fileExt string

	// logfile max backup time. equals Config.BackupTime * time.Hour
	backupDur time.Duration
	// oldFiles []string
	cleanCh chan struct{}
	stopCh  chan struct{}

	// context use for rotating file by size
	written   uint64 // written size
	rotateNum uint   // rotate times number

	// ---- context use for rotating file by time ----

	// the rotating file name suffix format. eg: "20210102", "20210102_1500"
	suffixFormat   string
	checkInterval  int64     // check interval seconds.
	nextRotatingAt time.Time // next rotating time
}

// NewWriter create rotate write with config and init it.
func NewWriter(c *Config) (*Writer, error) {
	d := &Writer{cfg: c}

	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

// NewWriterWith create a rotated writer with some settings.
func NewWriterWith(fns ...ConfigFn) (*Writer, error) {
	return NewWriter(NewConfigWith(fns...))
}

// init rotate dispatcher
func (d *Writer) init() error {
	d.id = fmt.Sprintf("%p", d)

	logfile := d.cfg.Filepath
	// dirSep := filepath.Separator
	// d.fileDir = filepath.Dir(logfile)
	d.fileDir, d.fileName = filepath.Split(d.cfg.Filepath)
	d.fileExt = filepath.Ext(d.fileName)                   // eg: .log
	d.onlyName = strings.TrimSuffix(d.fileName, d.fileExt) // eg: error
	// removes the trailing separator on the dir path
	if ln := len(d.fileDir); ln > 1 && d.fileDir[ln-1] == filepath.Separator {
		d.fileDir = d.fileDir[:ln-1]
	}

	d.backupDur = d.cfg.backupDuration()
	d.suffixFormat = d.cfg.RotateTime.TimeFormat()
	d.checkInterval = d.cfg.RotateTime.Interval()

	// calc and storage next rotating time
	if d.checkInterval > 0 {
		now := d.cfg.TimeClock.Now()
		// next rotating time
		d.nextRotatingAt = d.cfg.RotateTime.FirstCheckTime(now)
		if d.cfg.RotateMode == ModeCreate {
			// logfile = d.cfg.Filepath + "." + now.Format(d.suffixFormat)
			logfile = d.buildFilePath(now.Format(d.suffixFormat))
		} else if err := d.bootstrapMissedTimeRotate(); err != nil {
			// ModeRename 下，补偿关机期间错过的跨周期轮转，避免新日志追加进旧文件
			return err
		}
	}

	// open the current file
	return d.openFile(logfile)
}

// bootstrapMissedTimeRotate 在打开活跃文件之前，补偿进程停机期间错过的时间轮转。
//
// 背景：nextRotatingAt 总是按"启动时刻"重新计算为当前周期的结束边界，关机期间
// 错过的边界不会被补算。例如按天轮转时 11-10 关机、11-11 重启，若不补偿，10 日的
// error.log 不会被转存，11 日的新日志会继续追加进同一个文件。
//
// 规则：
//   - 仅 ModeRename 生效（ModeCreate 的活跃文件名本身带周期后缀，不存在跨周期追加）；
//   - 活跃文件最后修改时间落在当前周期之前：先重命名为它所属周期的备份文件，
//     随后 openFile 会创建一个全新的活跃文件；
//   - 文件不存在（首次启动）或仍属于当前周期：不做任何处理；
//   - rename 失败（Windows 上文件被其它进程占用）不阻断启动，退化为追加写。
func (d *Writer) bootstrapMissedTimeRotate() error {
	info, err := os.Stat(d.cfg.Filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}

	// 当前周期起点 = 当前周期结束边界 - 一个轮转间隔
	periodStart := d.nextRotatingAt.Add(-time.Duration(d.checkInterval) * time.Second)
	modTime := info.ModTime()
	if !modTime.Before(periodStart) {
		return nil // 文件属于当前周期，等待运行期正常轮转即可
	}

	// 用文件最后写入时间所在周期的结束边界生成备份名，
	// 与运行期 rotatingByTime 用 nextRotatingAt 命名的规则保持一致。
	suffix := d.cfg.RotateTime.FirstCheckTime(modTime).Format(d.suffixFormat)
	bakFile := d.uniqueBackupPath(d.buildFilePath(suffix))
	if renameErr := os.Rename(d.cfg.Filepath, bakFile); renameErr != nil {
		printErrln("rotatefile: bootstrap rename failed (fallback to append):", renameErr)
		return nil
	}
	d.debugLog("bootstrap rotate:", d.cfg.Filepath, "->", bakFile)
	return nil
}

// uniqueBackupPath 在目标备份路径已存在时追加 _2、_3 ... 序号，避免覆盖历史备份。
// 形如 som.log.20261110 -> som.log.20261110_2，后缀仍满足读取侧的
// 转存文件命名规则（<数字>[_<序号>]）。
func (d *Writer) uniqueBackupPath(target string) string {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return target
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", target, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// Config gets the config
func (d *Writer) Config() Config {
	return *d.cfg
}

// Flush sync data to disk. alias of Sync()
func (d *Writer) Flush() error {
	// 先同步数据到磁盘
	if err := d.file.Sync(); err != nil {
		return err
	}

	// 刷盘后检查是否需要轮转（因为实际文件大小可能已经超过限制）
	return d.doRotate()
}

// Sync data to disk.
func (d *Writer) Sync() error {
	return d.file.Sync()
}

// Close the writer. will sync data to disk, then close the file handle.
// and it will stop the async clean backups.
func (d *Writer) Close() error {
	if d.cfg.CleanOnClose {
		d.debugLog("cfg.CleanOnClose=true: start clean old files")
		printErrln("files-clear-onClose: cleanup old files error:", d.Clean())
	}

	return d.close(true)
}

// MustClose the writer. alias of Close(), but will panic if has error.
func (d *Writer) MustClose() {
	printErrln("rotatefile: close writer -", d.Close())
}

func (d *Writer) close(closeStopCh bool) error {
	if err := d.file.Sync(); err != nil {
		return err
	}

	// stop the async clean backups
	if closeStopCh && d.stopCh != nil {
		d.debugLog("close stopCh for stop async clean old files")
		close(d.stopCh)
		d.stopCh = nil
	}
	return d.file.Close()
}

//
// ---------------------------------------------------------------------------
// write and rotate file
// ---------------------------------------------------------------------------
//

// WriteString implements the io.StringWriter
func (d *Writer) WriteString(s string) (n int, err error) {
	return d.Write([]byte(s))
}

// Write data to file. then check and do rotate file, then async clean backups
func (d *Writer) Write(p []byte) (n int, err error) {
	// do write data
	if n, err = d.doWrite(p); err != nil {
		return
	}

	// do rotate file
	if rotErr := d.doRotate(); rotErr != nil {
		// rotate failure must NOT be returned to caller:
		// bufio/LineWriter permanently locks on any write error (b.err),
		// which would stop all future log writes. Log the error and continue.
		printErrln("rotatefile: rotate error (ignored to keep writing):", rotErr)
	}

	// async clean backup files.
	if d.shouldClean(true) {
		d.asyncClean()
	}
	return
}

func (d *Writer) doWrite(p []byte) (n int, err error) {
	// if enable lock
	if !d.cfg.CloseLock {
		d.mu.Lock()
		defer d.mu.Unlock()
	}

	n, err = d.file.Write(p)
	if err == nil {
		// update size
		d.written += uint64(n)
	}
	return
}


// Rotate the file by config and async clean backups
func (d *Writer) Rotate() error {
	err := d.doRotate()

	// async clean backup files.
	if err == nil && d.shouldClean(true) {
		d.asyncClean()
	}
	return err
}

// do rotate the logfile by config
func (d *Writer) doRotate() (err error) {
	// if enable lock
	if !d.cfg.CloseLock {
		d.mu.Lock()
		defer d.mu.Unlock()
	}

	// do rotate a file by size
	// Note: We check > instead of >= to avoid double rotation
	// since Write() now pre-checks before writing
	if d.cfg.MaxSize > 0 && d.written > d.cfg.MaxSize {
		err = d.rotatingBySize()
		if err != nil {
			return
		}
	}

	// do rotate a file by time
	if d.checkInterval > 0 && d.written > 0 {
		err = d.rotatingByTime()
	}
	return
}

// TIP: should only call on d.checkInterval > 0
func (d *Writer) rotatingByTime() error {
	now := d.cfg.TimeClock.Now()
	if now.Before(d.nextRotatingAt) {
		return nil
	}

	// generate new file path.
	// eg: /tmp/error.log => /tmp/error.20220423_1600.log
	// file := d.cfg.Filepath + "." + d.nextRotatingAt.Format(d.suffixFormat)
	file := d.buildFilePath(d.nextRotatingAt.Format(d.suffixFormat))
	err := d.rotatingFile(file, false)

	// calc and storage next rotating time
	d.nextRotatingAt = d.nextRotatingAt.Add(time.Duration(d.checkInterval) * time.Second)
	return err
}

func (d *Writer) rotatingBySize() error {
	d.rotateNum++
	now := d.cfg.TimeClock.Now()
	// up: use now minutes + seconds as rotate number
	numStr := fmt.Sprintf("%d%d%d", now.Hour(), now.Minute(), now.Second())
	numInt := strutil.IntOr(numStr, 0) + now.Nanosecond()/1000
	rotateNum := uint(numInt) + d.rotateNum

	var bakFile string
	if d.cfg.IsMode(ModeCreate) {
		// eg: /tmp/error.log => /tmp/error.894136.log
		// eg: /tmp/error.20220423_1600.log => /tmp/error.20220423_1600_894136.log
		pathNoExt := d.path[:len(d.path)-len(d.fileExt)]
		bakFile = fmt.Sprintf("%s_%d%s", pathNoExt, rotateNum, d.fileExt)
	} else if d.cfg.RenameFunc != nil {
		// rename current to new file by custom RenameFunc
		// eg: /tmp/error.log => /tmp/error.163021_894136.log
		bakFile = d.cfg.RenameFunc(d.cfg.Filepath, rotateNum)
	} else {
		// eg: /tmp/error.log => /tmp/error.25031615_894136.log
		bakFile = d.buildFilePath(fmt.Sprintf("%s_%d", now.Format("06010215"), rotateNum))
	}

	// always rename current to a new file
	return d.rotatingFile(bakFile, true)
}

// rotateFile closes the syncBuffer's file and starts a new one.
func (d *Writer) rotatingFile(bakFile string, rename bool) error {
	// close the current file
	if err := d.close(false); err != nil {
		return err
	}

	// record old files for clean.
	// d.oldFiles = append(d.oldFiles, bakFile)

	// rename current to a new file.
	if rename || d.cfg.RotateMode == ModeRename {
		if err := os.Rename(d.path, bakFile); err != nil {
			// Windows 上文件可能仍被其它进程（日志采集 agent、杀软、重复实例）
			// 占用而 rename 失败。此时 d.file 已在上面 close，若直接返回，后续写入
			// 会全部作用在已关闭句柄上，且错误被上层 bufio 静默吞掉，表现为日志
			// 彻底停写直到重启。这里重开原文件恢复句柄，退化为继续追加写，
			// 并保留真实文件大小，等待下一次写入/Flush 再尝试轮转。
			if reopenErr := d.openFile(d.path); reopenErr != nil {
				return reopenErr
			}
			return err
		}
	}

	// filepath for reopening
	logfile := d.path
	if d.cfg.RotateMode == ModeRename {
		logfile = d.cfg.Filepath
	}

	// reopen log file
	if err := d.openFile(logfile); err != nil {
		return err
	}

	// reset written
	d.written = 0
	return nil
}

//
// ---------------------------------------------------------------------------
// clean backup files
// ---------------------------------------------------------------------------
//

// check should clean old files by config
func (d *Writer) shouldClean(withRand bool) bool {
	cfgIsYes := d.cfg.BackupNum > 0 || d.cfg.BackupTime > 0
	if !withRand {
		return cfgIsYes
	}

	// 20% probability trigger clean
	return cfgIsYes && rand.Intn(100) < 20
}

// async clean old files by config. should be in lock.
func (d *Writer) asyncClean() {
	if !d.shouldClean(false) {
		return
	}

	// if already running, send a signal
	if d.cleanCh != nil {
		d.notifyClean()
		return
	}

	// double-checked locking: must use exclusive Lock (not RLock),
	// so only one goroutine can pass the re-check and initialize channels.
	d.mu.Lock()
	defer d.mu.Unlock()

	// re-check d.cleanCh is not nil
	if d.cleanCh != nil {
		d.notifyClean()
		return
	}

	// init clean channel
	d.debugLog("INIT clean and stop channels for clean old files")
	d.cleanCh = make(chan struct{}, 1)
	d.stopCh = make(chan struct{})

	// start a goroutine to clean backups
	go func() {
		d.debugLog("START a goroutine consumer for clean old files")

		// consume the signal until stop
		for {
			select {
			case <-d.cleanCh:
				d.debugLog("receive signal - clean old files handling")
				printErrln("rotatefile: clean old files error:", d.doClean())
			case <-d.stopCh:
				d.cleanCh = nil
				d.debugLog("STOP consumer for clean old files")
				return // stop clean
			}
		}
	}()
}

func (d *Writer) notifyClean() {
	select {
	case d.cleanCh <- struct{}{}: // notify clean old files
		d.debugLog("sent signal - start clean old files...")
	default: // skip on blocking
		d.debugLog("clean old files signal blocked, SKIP")
	}
}

// Clean old files by config
func (d *Writer) Clean() (err error) {
	if d.cfg.BackupNum == 0 && d.cfg.BackupTime == 0 {
		return errorx.Err("clean: backupNum and backupTime are both 0")
	}

	// up: 单独运行清理，不需要设置 skipSeconds
	return d.doClean(0)
}

// do clean old files by config
//
// - skipSeconds: skip find files that are within the specified seconds
func (d *Writer) doClean(skipSeconds ...int) (err error) {
	// oldFiles: xx.log.yy files, no gz file
	var oldFiles, gzFiles []fileInfo
	fileDir, fileName := d.fileDir, d.fileName
	curFileName := filepath.Base(d.path)

	// FIX: do not process recent changes to avoid conflicts
	skipSec := 30
	if len(skipSeconds) > 0 {
		skipSec = skipSeconds[0]
	}
	limitTime := d.cfg.TimeClock.Now().Add(-time.Second * time.Duration(skipSec))

	// find and clean old files
	d.debugLog("clean - find old files, match name:", fileName, ", in dir:", fileDir)
	err = fsutil.FindInDir(fileDir, func(fPath string, ent fs.DirEntry) error {
		fi, err := ent.Info()
		if err != nil {
			return err
		}

		// fix: exclude the current file
		if ent.Name() == curFileName {
			return nil
		}

		if strings.HasSuffix(ent.Name(), compressSuffix) {
			gzFiles = append(gzFiles, newFileInfo(fPath, fi))
		} else if fi.ModTime().Before(limitTime) {
			oldFiles = append(oldFiles, newFileInfo(fPath, fi))
		}
		return nil
	}, d.buildFilterFns(fileName)...)

	gzNum := len(gzFiles)
	oldNum := len(oldFiles)
	remNum := mathutil.Max(gzNum+oldNum-int(d.cfg.BackupNum), 0)
	d.debugLog("clean old files, gzNum:", gzNum, "oldNum:", oldNum, "remNum:", remNum)

	if remNum > 0 && d.cfg.BackupNum > 0 {
		// remove old gz files
		if gzNum > 0 {
			remNum, err = d.removeOldGzFiles(remNum, gzFiles)
			if err != nil {
				return err
			}
		}

		// remove old log files
		if remNum > 0 && oldNum > 0 {
			oldFiles, err = d.removeOldFiles(remNum, oldFiles)
			if err != nil {
				return err
			}
		}
	}

	if d.cfg.Compress && len(oldFiles) > 0 {
		d.debugLog("compress old normal files to gz files")
		err = d.compressFiles(oldFiles)
	}
	return
}

// remove old gz files
func (d *Writer) removeOldGzFiles(remNum int, gzFiles []fileInfo) (rn int, err error) {
	gzNum := len(gzFiles)
	sort.Sort(modTimeFInfos(gzFiles)) // sort by mod-time
	d.debugLog("remove old gz files ...")

	for idx := 0; idx < gzNum; idx++ {
		d.debugLog("remove old gz file:", gzFiles[idx].filePath)
		if err = os.Remove(gzFiles[idx].filePath); err != nil {
			break
		}

		remNum--
		if remNum == 0 {
			break
		}
	}

	if err != nil {
		return remNum, errorx.Wrap(err, "remove old gz file error")
	}
	return remNum, nil
}

// remove old log files
func (d *Writer) removeOldFiles(remNum int, oldFiles []fileInfo) (files []fileInfo, err error) {
	// sort by mod-time, oldest at first.
	sort.Sort(modTimeFInfos(oldFiles))
	d.debugLog("remove old normal files ...")

	var idx int
	oldNum := len(oldFiles)

	for idx = 0; idx < oldNum; idx++ {
		d.debugLog("remove old file:", oldFiles[idx].filePath)
		if err = os.Remove(oldFiles[idx].filePath); err != nil {
			break
		}

		remNum--
		if remNum == 0 {
			break
		}
	}

	oldFiles = oldFiles[idx+1:]
	if err != nil {
		return nil, errorx.Wrap(err, "remove old file error")
	}
	return oldFiles, nil
}

//
// ---------------------------------------------------------------------------
// helper methods
// ---------------------------------------------------------------------------
//

// open the current file. and set the d.file, d.path
func (d *Writer) openFile(logfile string) error {
	file, err := fsutil.OpenFile(logfile, DefaultFileFlags, d.cfg.FilePerm)
	if err != nil {
		return err
	}

	d.path = logfile
	d.file = file

	// O_APPEND 打开一个已存在的文件时，用其真实大小初始化 written。
	// 否则进程重启后 written 从 0 重新计数，尺寸轮转要再写满一个 MaxSize
	// 才会触发，磁盘上的实际文件可能涨到近两倍阈值仍不转存。
	// 注意：rotatingFile 成功新建文件后会显式把 written 重置为 0，不受影响。
	if fi, statErr := file.Stat(); statErr == nil {
		d.written = uint64(fi.Size())
	}
	return nil
}

// return eg. logs/error.20220423_1600.log
func (d *Writer) buildFilePath(suffix string) string {
	fileName := d.fileName + "." + suffix
	return fmt.Sprintf("%s/%s", d.fileDir, fileName)
}

func (d *Writer) buildFilterFns(fileName string) []fsutil.FilterFunc {
	onlyName := d.fileName
	filterFns := []fsutil.FilterFunc{
		fsutil.OnlyFindFile,
		// filter by name. match pattern like: error.log.* eg: error.log.xx, error.log.xx.gz
		func(fPath string, ent fs.DirEntry) bool {
			// ok, _ := path.Match(fileName+".*", ent.Name())
			if !strings.HasPrefix(ent.Name(), fileName) {
				// 自定义文件名 eg: error.log -> error.20220423_02.log
				return strings.HasPrefix(ent.Name(), onlyName)
			}
			return true
		},
	}

	// filter by mod-time, clear expired files
	if d.cfg.BackupTime > 0 {
		cutTime := d.cfg.TimeClock.Now().Add(-d.backupDur)
		filterFns = append(filterFns, func(fPath string, ent fs.DirEntry) bool {
			fi, err := ent.Info()
			if err != nil {
				return false // skip, not handle
			}

			// collect un-expired
			if fi.ModTime().After(cutTime) {
				return true
			}

			// remove expired files
			d.debugLog("remove expired file:", fPath)
			printErrln("rotatefile: remove expired file error:", os.Remove(fPath))
			return false
		})
	}

	return filterFns
}

func (d *Writer) compressFiles(oldFiles []fileInfo) error {
	for _, fi := range oldFiles {
		err := compressFile(fi.filePath, fi.filePath+compressSuffix)
		if err != nil {
			return errorx.Wrap(err, "compress old file error")
		}

		// remove an old log file
		d.debugLog("compress and rm old file:", fi.filePath)
		if err = os.Remove(fi.filePath); err != nil {
			return errorx.Wrap(err, "remove file error after compress")
		}
	}
	return nil
}

// Debug print debug message on development
func (d *Writer) debugLog(vs ...any) {
	if d.cfg.DebugMode {
		stdio.WriteString("[rotatefile.DEBUG] ID:" + d.id + " | " + fmt.Sprintln(vs...))
	}
}
