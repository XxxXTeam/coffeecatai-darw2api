//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

/* setSysProcAttr 设置 Unix 进程组属性，使子进程成为独立进程组 */
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

/* killProcessTree 杀掉整个进程组 */
func killProcessTree(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
