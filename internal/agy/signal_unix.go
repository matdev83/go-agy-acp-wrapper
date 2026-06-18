//go:build !windows

package agy

import (
	"os"
	"syscall"
	"time"
)

func gracefulKill(process *os.Process, gracePeriod time.Duration) error {
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return process.Kill()
	}

	done := make(chan struct{})
	go func() {
		process.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(gracePeriod):
		return process.Kill()
	}
}
