//go:build windows

package main

import (
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

/* setSysProcAttr 设置 Windows 进程组属性，使子进程不继承控制台信号 */
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

/* killProcessTree 杀掉整个进程树（含浏览器子进程） */
func killProcessTree(cmd *exec.Cmd) {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", cmd.Process.Pid))
	kill.Stdout = io.Discard
	kill.Stderr = io.Discard
	_ = kill.Run()
}
