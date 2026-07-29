//go:build windows

package agy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	processHelperEnv  = "GO_AGY_PROCESS_HELPER"
	processSpecEnv    = "GO_AGY_PROCESS_SPEC"
	processGateEnv    = "GO_AGY_PROCESS_GATE"
	processGateWait   = 100 * time.Millisecond
	processGateExpiry = 30 * time.Second
)

type helperProcessSpec struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

type processTreeController struct {
	job       windows.Handle
	gatePath  string
	closeOnce sync.Once
	closeErr  error
}

func init() {
	if os.Getenv(processHelperEnv) != "1" || os.Getenv(processSpecEnv) == "" || os.Getenv(processGateEnv) == "" {
		return
	}
	os.Exit(runProcessHelper())
}

func runProcessHelper() int {
	var spec helperProcessSpec
	if err := json.Unmarshal([]byte(os.Getenv(processSpecEnv)), &spec); err != nil {
		fmt.Fprintf(os.Stderr, "go-agy process helper: invalid spec: %v\n", err)
		return 125
	}
	gate := os.Getenv(processGateEnv)
	deadline := time.Now().Add(processGateExpiry)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "go-agy process helper: gate wait timed out")
			return 124
		}
		time.Sleep(processGateWait)
	}

	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "go-agy process helper: execute command: %v\n", err)
		return 126
	}
	return 0
}

func configureProcessTree(cmd *exec.Cmd) (*processTreeController, error) {
	cmd.WaitDelay = 5 * time.Second
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}

	spec, err := json.Marshal(helperProcessSpec{Path: cmd.Path, Args: cmd.Args[1:], Env: cmd.Env, Dir: cmd.Dir})
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	var gateID [16]byte
	if _, err := io.ReadFull(rand.Reader, gateID[:]); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	gatePath := filepath.Join(os.TempDir(), "go-agy-process-gate-"+hex.EncodeToString(gateID[:]))
	helper, err := os.Executable()
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	cmd.Path = helper
	cmd.Args = []string{helper}
	cmd.Env = append(os.Environ(), processHelperEnv+"=1", processSpecEnv+"="+string(spec), processGateEnv+"="+gatePath)

	controller := &processTreeController{job: job, gatePath: gatePath}
	cmd.Cancel = controller.Close
	return controller, nil
}

func (c *processTreeController) AfterStart(cmd *exec.Cmd) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(c.job, process); err != nil {
		return err
	}
	return os.WriteFile(c.gatePath, nil, 0600)
}

func (c *processTreeController) Close() error {
	c.closeOnce.Do(func() {
		_ = os.Remove(c.gatePath)
		c.closeErr = windows.CloseHandle(c.job)
		if errors.Is(c.closeErr, windows.ERROR_INVALID_HANDLE) {
			c.closeErr = nil
		}
	})
	return c.closeErr
}
