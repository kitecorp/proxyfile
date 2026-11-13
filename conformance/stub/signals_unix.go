//go:build unix

package main

import (
	"os"
	"syscall"
)

// refuseSignal makes a backend refuse connections and acceptSignal makes it listen again (serve).
var refuseSignal, acceptSignal os.Signal = syscall.SIGUSR1, syscall.SIGUSR2
