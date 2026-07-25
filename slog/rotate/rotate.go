/*
 * Copyright (C) 2019 KQ GEO Technologies Co., Ltd.
 * All rights reserved.
 */

package rotate

import (
	"github.com/gookit/goutil/errorx"
	"github.com/gookit/goutil/fsutil"
	"github.com/gookit/goutil/x/basefn"
	"github.com/gookit/slog/bufwrite"
	"github.com/gookit/slog/handler"
	"github.com/gookit/slog/rotatefile"
	"io"
)

// RotateWriter interface
type RotateWriter interface {
	io.WriteCloser
	Clean() error
	Flush() error
	Rotate() error
	Sync() error
}

const (
	// OneMByte size
	OneMByte uint64 = 1024 * 1024

	// DefaultMaxSize of a log file. default is 20M.
	DefaultMaxSize = 20 * OneMByte
	// DefaultBackNum default backup numbers for old files.
	DefaultBackNum uint = 20
	// DefaultBackTime default backup time for old files. default keeps a week.
	DefaultBackTime uint = 24 * 7
)

// NewRotateFileHandler instance. It supports splitting log files by time and size
func NewRotateFileHandler(logfile string, rt rotatefile.RotateTime, fns ...handler.ConfigFn) (*handler.SyncCloseHandler, error) {
	cfg := handler.NewConfig(fns...).With(handler.WithLogfile(logfile), handler.WithRotateTime(rt))

	writer, err := NewRotateWriter(cfg)
	if err != nil {
		return nil, err
	}

	h := handler.NewSyncCloseHandler(writer, cfg.Levels)
	return h, nil
}

func NewRotateWriter(c *handler.Config) (output handler.SyncCloseWriter, err error) {
	if c.MaxSize == 0 && c.RotateTime == 0 {
		return nil, errorx.E("slog: cannot create rotate writer, MaxSize and RotateTime both is 0")
	}
	if c.Logfile == "" {
		return nil, errorx.Raw("slog: logfile cannot be empty for create writer")
	}
	if c.FilePerm == 0 {
		c.FilePerm = DefaultFilePerm
	}

	// create a rotated writer by config.
	if c.MaxSize > 0 || c.RotateTime > 0 {
		rc := EmptyConfigWith()

		// has locked on logger.write()
		rc.CloseLock = true
		rc.Filepath = c.Logfile
		rc.FilePerm = c.FilePerm
		rc.DebugMode = c.DebugMode

		// copy settings
		rc.MaxSize = c.MaxSize
		rc.RotateTime = RotateTime(c.RotateTime)
		rc.RotateMode = RotateMode(c.RotateMode)
		rc.BackupNum = c.BackupNum
		rc.BackupTime = c.BackupTime
		rc.Compress = c.Compress
		rc.CleanOnClose = c.CleanOnClose

		if c.RenameFunc != nil {
			rc.RenameFunc = c.RenameFunc
		}
		if c.TimeClock != nil {
			rc.TimeClock = c.TimeClock
		}

		output, err = rc.Create()
	} else {
		// create a file writer
		output, err = fsutil.OpenAppendFile(c.Logfile, c.FilePerm)
	}

	if err != nil {
		return nil, err
	}

	// wrap buffer
	if c.BuffSize > 0 {
		if c.BuffMode == handler.BuffModeLine {
			output = bufwrite.NewLineWriterSize(output, c.BuffSize)
		} else {
			output = bufwrite.NewBufIOWriterSize(output, c.BuffSize)
		}
	}
	return
}

// MustRotateFile handler instance, will panic on create error
func MustRotateFile(logfile string, rt rotatefile.RotateTime, fns ...handler.ConfigFn) *handler.SyncCloseHandler {
	return basefn.Must(NewRotateFileHandler(logfile, rt, fns...))
}
