//go:build !unix

package main

import "os"

// A system without SIGUSR1 and SIGUSR2 has neither signal, so serve watches none and a backend never refuses
// connections. The stub runs in a Linux container; this file keeps go build ./... working on every system.
var refuseSignal, acceptSignal os.Signal
