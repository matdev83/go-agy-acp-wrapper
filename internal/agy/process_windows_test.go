//go:build windows

package agy

import (
	"os/exec"
	"testing"
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
}
