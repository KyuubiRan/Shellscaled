// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
// Synthetic daemon/receiver for the emulator-only live-update tests.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--socket=") {
			listener, err := net.Listen("unix", strings.TrimPrefix(arg, "--socket="))
			if err != nil {
				fmt.Fprintln(os.Stderr, "fixture Unix listener:", err)
				os.Exit(1)
			}
			defer listener.Close()
		}
	}
	<-stop
}
