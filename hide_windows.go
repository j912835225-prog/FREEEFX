package main

import (
	"os/exec"
	"syscall"
)

// hideWindow 让 ffmpeg 在 Windows 上不闪黑框。
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
