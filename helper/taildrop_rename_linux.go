// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

// Linux UAPI syscall numbers, also verified in the installed Android NDK's
// asm/unistd headers. syscall's frozen amd64/arm tables omit renameat2.
func renameTaildropNoReplace(old, target string) error {
	var number uintptr
	switch runtime.GOARCH {
	case "arm64":
		number = 276
	case "arm":
		number = 382
	case "amd64":
		number = 316
	default:
		return syscall.ENOSYS
	}
	from, err := syscall.BytePtrFromString(old)
	if err != nil {
		return err
	}
	to, err := syscall.BytePtrFromString(target)
	if err != nil {
		return err
	}
	const atFDCWD = ^uintptr(99) // -100
	const renameNoReplace = 1
	_, _, errno := syscall.Syscall6(number, atFDCWD, uintptr(unsafe.Pointer(from)), atFDCWD, uintptr(unsafe.Pointer(to)), renameNoReplace, 0)
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	if errno != 0 {
		return errno
	}
	return nil
}
