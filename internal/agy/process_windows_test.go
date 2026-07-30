//go:build windows

package agy

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHideWindow(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo test")
	hideWindow(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("expected SysProcAttr to be set")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("expected HideWindow to be true")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Error("expected CREATE_NO_WINDOW flag to be set")
	}
}
