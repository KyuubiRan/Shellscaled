// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Explicit opt-in only, invoked through scripts/emulator.py on Medium_Phone.
// Never touches actual Taildrop files or another directory's entries.
func TestTaildropAndroidSharedStorage(t *testing.T) {
	if os.Getenv("SHELLSCALED_STORAGE_TEST") != "1" {
		t.Skip("shared storage test requires explicit opt-in")
	}
	qemu, err := exec.Command("/system/bin/getprop", "ro.kernel.qemu").Output()
	if err != nil || strings.TrimSpace(string(qemu)) != "1" {
		t.Fatal("shared storage test refuses non-emulators")
	}
	enforcing, err := os.ReadFile("/sys/fs/selinux/enforce")
	if err != nil || strings.TrimSpace(string(enforcing)) != "1" {
		t.Fatal("shared storage test requires SELinux Enforcing")
	}
	const download = "/storage/emulated/0/Download"
	info, err := os.Lstat(download)
	if err != nil || !info.IsDir() {
		t.Fatal("existing Download directory is required")
	}
	directory, err := os.MkdirTemp(download, ".shellscaled-storage-test-")
	if err != nil {
		t.Fatal("cannot create task-owned storage fixture")
	}
	// Keep its original backing identity and remove only this exact fixture.
	identity, err := taildropIdentity(directory)
	if err != nil {
		t.Fatal("cannot identify task-owned storage fixture")
	}
	defer func() {
		current, err := taildropIdentity(directory)
		if err == nil && current == identity {
			if err := os.RemoveAll(directory); err != nil {
				t.Error("task-owned fixture cleanup failed")
			}
		} else {
			t.Error("fixture identity changed; refusing recursive cleanup")
		}
	}()
	backing, _ := sharedTaildropBacking(directory)
	if err := os.Chown(backing, 1023, 1023); err != nil {
		t.Fatal("fixture ownership setup failed")
	}
	if err := os.Chmod(backing, 0775); err != nil {
		t.Fatal("fixture permissions setup failed")
	}
	if err := restoreMediaLabel(backing); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	const name = "synthetic.txt"
	if err := os.WriteFile(filepath.Join(staging, name), []byte("new payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("existing payload"), 0600); err != nil {
		t.Fatal(err)
	}
	existing, err := taildropIdentity(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	normalize := func(path string) error {
		if _, err := os.Stat(filepath.Join(directory, "synthetic (1).txt")); !os.IsNotExist(err) {
			t.Fatal("final name appeared before validation")
		}
		return normalizePublishedTaildropFile(path)
	}
	if n, err := publishTaildropStagingWith(staging, directory, normalize); err != nil || n != 1 {
		t.Fatalf("FUSE publication failed: count=%d err=%v", n, err)
	}
	if current, err := taildropIdentity(filepath.Join(directory, name)); err != nil || current != existing {
		t.Fatal("FUSE publication replaced an existing inode")
	}
	if data, err := os.ReadFile(filepath.Join(directory, name)); err != nil || string(data) != "existing payload" {
		t.Fatal("existing shared file changed")
	}
	published := filepath.Join(directory, "synthetic (1).txt")
	if data, err := os.ReadFile(published); err != nil || string(data) != "new payload" {
		t.Fatal("published shared payload changed")
	}
	publishedBacking, _ := sharedTaildropBacking(published)
	if err := verifyMediaOwner(publishedBacking); err != nil {
		t.Fatal(err)
	}
	label := make([]byte, 512)
	n, err := syscall.Getxattr(publishedBacking, "security.selinux", label)
	if err != nil || !strings.Contains(string(label[:max(n, 0)]), ":media_rw_data_file:") {
		t.Fatal("published storage label is invalid")
	}

	temporary, file, err := createTaildropPublication(staging, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("partial synthetic bytes"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	unrelated := filepath.Join(directory, ".shellscaled-taildrop-ffffffffffffffffffffffffffffffff.partial")
	if err := os.WriteFile(unrelated, []byte("unrecorded"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("recorded FUSE temporary file survived recovery")
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "unrecorded" {
		t.Fatal("unrecorded FUSE file was touched")
	}
	enforcing, err = os.ReadFile("/sys/fs/selinux/enforce")
	if err != nil || strings.TrimSpace(string(enforcing)) != "1" {
		t.Fatal("SELinux did not remain Enforcing")
	}
}
