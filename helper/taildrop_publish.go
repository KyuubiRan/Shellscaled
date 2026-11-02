// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// A private record names one exact public temporary file. Recovery never scans
// public prefixes, and never deletes an entry whose recorded identity changed.
type taildropFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Kind   uint32 `json:"kind"`
}

type taildropPublication struct {
	Version   int                  `json:"version"`
	Directory string               `json:"directory"`
	Temporary string               `json:"temporary"`
	Parent    taildropFileIdentity `json:"parent"`
	File      taildropFileIdentity `json:"file"`
}

var renameTaildropFile = renameTaildropNoReplace // Internal filesystem-failure test hook.

var taildropTemporaryName = regexp.MustCompile(`^\.shellscaled-taildrop-[0-9a-f]{32}\.partial$`)

func taildropPublicationPath(staging string) string { return staging + ".publish.json" }

func taildropIdentity(path string) (taildropFileIdentity, error) {
	// FUSE inode numbers need not survive a remount. The backing inode is stable
	// across a receiver restart/reboot and normalization changes only metadata.
	if backing, ok := sharedTaildropBacking(path); ok {
		path = backing
	}
	info, err := os.Lstat(path)
	if err != nil {
		return taildropFileIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return taildropFileIdentity{}, errors.New("symlink")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return taildropFileIdentity{}, errors.New("file identity unavailable")
	}
	return taildropFileIdentity{uint64(st.Dev), st.Ino, uint32(info.Mode().Type())}, nil
}

func syncTaildropDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func writeTaildropAtomic(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".new-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	return syncTaildropDirectory(filepath.Dir(path))
}

func saveTaildropPublication(staging string, record taildropPublication) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeTaildropAtomic(taildropPublicationPath(staging), data)
}

func clearTaildropPublication(staging string) error {
	if err := os.Remove(taildropPublicationPath(staging)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncTaildropDirectory(filepath.Dir(staging))
}

func verifyTaildropPublication(staging, temporary string) error {
	file, err := os.Open(taildropPublicationPath(staging))
	if err != nil {
		return err
	}
	defer file.Close()
	var record taildropPublication
	if err := json.NewDecoder(io.LimitReader(file, 4096)).Decode(&record); err != nil {
		return err
	}
	if record.Version != 1 || !taildropTemporaryName.MatchString(record.Temporary) ||
		filepath.Join(record.Directory, record.Temporary) != temporary || record.File.Inode == 0 || record.File.Kind != 0 {
		return errors.New("invalid publication identity")
	}
	parent, err := taildropIdentity(record.Directory)
	if err != nil || parent != record.Parent {
		return errors.New("publication directory identity changed")
	}
	identity, err := taildropIdentity(temporary)
	if err != nil || identity != record.File {
		return errors.New("publication file identity changed")
	}
	return nil
}

func cleanupTaildropPublication(staging string) error {
	journal, err := os.Open(taildropPublicationPath(staging))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer journal.Close()
	var record taildropPublication
	decoder := json.NewDecoder(io.LimitReader(journal, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return err
	}
	if record.Version != 1 || !filepath.IsAbs(record.Directory) || filepath.Clean(record.Directory) != record.Directory ||
		!taildropTemporaryName.MatchString(record.Temporary) || record.Parent.Inode == 0 || record.Parent.Kind != uint32(os.ModeDir) {
		return errors.New("invalid publication record")
	}
	parent, err := taildropIdentity(record.Directory)
	if err != nil || parent != record.Parent {
		return errors.New("publication directory identity changed")
	}
	// No bytes are copied until the created file's identity is durable. A kill
	// in that small initial window can leave an empty hidden entry; do not guess
	// its ownership or delete another app's entry at the same name.
	if record.File.Inode == 0 {
		return clearTaildropPublication(staging)
	}
	if record.File.Kind != 0 {
		return errors.New("publication is not a regular file")
	}
	temporary := filepath.Join(record.Directory, record.Temporary)
	identity, err := taildropIdentity(temporary)
	if os.IsNotExist(err) {
		return clearTaildropPublication(staging)
	}
	if err != nil || identity != record.File {
		return errors.New("publication file identity changed")
	}
	// Check the public name too; never follow a link in the selected directory.
	info, err := os.Lstat(temporary)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("publication file type changed")
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	if err := syncTaildropDirectory(record.Directory); err != nil {
		return err
	}
	return clearTaildropPublication(staging)
}

func createTaildropPublication(staging, directory string) (string, *os.File, error) {
	parent, err := taildropIdentity(directory)
	if err != nil || parent.Kind != uint32(os.ModeDir) {
		return "", nil, errors.New("invalid publication directory")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", nil, err
	}
	name := ".shellscaled-taildrop-" + hex.EncodeToString(nonce[:]) + ".partial"
	record := taildropPublication{Version: 1, Directory: directory, Temporary: name, Parent: parent}
	if err := saveTaildropPublication(staging, record); err != nil {
		return "", nil, err
	}
	temporary := filepath.Join(directory, name)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = clearTaildropPublication(staging)
		return "", nil, err
	}
	record.File, err = taildropIdentity(temporary)
	if err == nil {
		err = saveTaildropPublication(staging, record)
	}
	if err != nil {
		_ = file.Close()
		// Ownership is known in this process even if recording it failed.
		if current, statErr := taildropIdentity(temporary); statErr == nil && current == record.File {
			_ = os.Remove(temporary)
		}
		_ = clearTaildropPublication(staging)
		return "", nil, err
	}
	return temporary, file, nil
}

func publishTaildropStagingWith(staging, directory string, normalize func(string) error) (int, error) {
	failure := func(count int) (int, error) {
		return count, errors.New("无法完整写入接收文件，文件已保留")
	}
	if err := cleanupTaildropPublication(staging); err != nil {
		return failure(0)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return 0, errors.New("无法读取 Taildrop 临时目录")
	}
	if len(entries) == 0 {
		return 0, errors.New("当前没有可接收的文件")
	}
	count := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			return count, errors.New("Taildrop 临时目录包含非普通文件，文件已保留")
		}
		source := filepath.Join(staging, entry.Name())
		if resolved, err := filepath.EvalSymlinks(source); err != nil || resolved != source {
			return count, errors.New("Taildrop 文件路径无效，文件已保留")
		}
		temporary, file, err := createTaildropPublication(staging, directory)
		if err != nil {
			return failure(count)
		}
		in, err := os.Open(source)
		if err == nil {
			_, err = io.Copy(file, in)
			if closeErr := in.Close(); err == nil {
				err = closeErr
			}
		}
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = normalize(temporary)
		}
		// Persist metadata changes made by normalization before the final name.
		if err == nil {
			if checked, openErr := os.Open(temporary); openErr != nil {
				err = openErr
			} else {
				err = checked.Sync()
				if closeErr := checked.Close(); err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			_ = cleanupTaildropPublication(staging)
			return failure(count)
		}
		// Validation must still refer to the exact file we downloaded. A
		// replacement at the public temporary name must never be committed.
		if err := verifyTaildropPublication(staging, temporary); err != nil {
			return failure(count)
		}
		published := false
		for attempt := 0; attempt < 100; attempt++ {
			target := filepath.Join(directory, numberedTaildropName(entry.Name(), attempt))
			err = renameTaildropFile(temporary, target)
			if err == nil {
				published = true
				break
			}
			if !errors.Is(err, os.ErrExist) {
				break
			}
		}
		if !published {
			_ = cleanupTaildropPublication(staging)
			return failure(count)
		}
		count++
		// Never remove a completed public file to roll back an ambiguous sync.
		// Retaining staging may create a complete duplicate after a crash.
		if err := syncTaildropDirectory(directory); err != nil {
			return failure(count)
		}
		if err := clearTaildropPublication(staging); err != nil {
			return failure(count)
		}
		if err := os.Remove(source); err != nil {
			return count, errors.New("接收文件已写入但临时文件未清理")
		}
		if err := syncTaildropDirectory(staging); err != nil {
			return failure(count)
		}
	}
	return count, nil
}
