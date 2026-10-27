//go:build !darwin && !windows

package main

import (
	"errors"

	"github.com/egoist/mygo"
)

// 只做了 Mac（AppKit）与 Windows（OLE）的拖出。
func dragFiles(win *mygo.Window, paths []string, done func(dropped bool)) error {
	return errors.New("这个系统上还不能拖出")
}
