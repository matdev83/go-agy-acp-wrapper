//go:build windows

package agy

import (
	"os"
	"time"
)

func gracefulKill(process *os.Process, _ time.Duration) error {
	return process.Kill()
}
