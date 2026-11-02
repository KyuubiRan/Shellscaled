//go:build !linux

// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import "syscall"

func renameTaildropNoReplace(_, _ string) error { return syscall.ENOSYS }
