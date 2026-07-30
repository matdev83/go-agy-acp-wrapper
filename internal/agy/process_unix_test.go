//go:build !windows

package agy

import (
	"os/exec"
	"testing"
)

func TestHideWindowIsNoop(t *testing.T) {
	cmd := exec.Command("echo", "test")
	hideWindow(cmd)
	if cmd.SysProcAttr != nil {
		t.Error("expected SysProcAttr to remain nil on non-Windows")
	}
}
