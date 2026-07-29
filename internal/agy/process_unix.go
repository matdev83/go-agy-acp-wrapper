//go:build !windows

package agy

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processTreeController struct{}

func configureProcessTree(cmd *exec.Cmd) (*processTreeController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return &processTreeController{}, nil
}

func (*processTreeController) AfterStart(*exec.Cmd) error { return nil }
func (*processTreeController) Close() error               { return nil }
