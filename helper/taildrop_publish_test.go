// Synthetic filesystem and LocalAPI regressions; run only on Medium_Phone.
// These use synthetic in-memory LocalAPI replies and t.TempDir storage only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestTaildropDeleteLostAckPreservesReusedName(t *testing.T) {
	for _, replacement := range []string{"new", "new-generation"} {
		t.Run(fmt.Sprintf("replacement-size-%d", len(replacement)), func(t *testing.T) {
			assertTaildropDeleteReusedName(t, replacement)
		})
	}
}

func assertTaildropDeleteReusedName(t *testing.T, replacement string) {
	staging, partial := t.TempDir(), filepath.Join(t.TempDir(), "partial")
	const name = "synthetic.txt"
	original := "old"
	daemon := original
	b := &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		res := &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: -1}
		switch r.Method + " " + r.URL.Path {
		case "GET /localapi/v0/files/":
			body = fmt.Sprintf(`[{"Name":%q,"Size":%d}]`, name, len(daemon))
		case "GET /localapi/v0/files/" + name:
			body, res.ContentLength = daemon, int64(len(daemon))
		case "DELETE /localapi/v0/files/" + name:
			// DELETE has committed, then another transfer reuses the now-free
			// basename before the receiver can re-query after losing the ack.
			daemon = replacement
			return nil, errors.New("synthetic lost DELETE acknowledgement")
		default:
			t.Fatal("unexpected synthetic request")
		}
		res.Body = io.NopCloser(strings.NewReader(body))
		return res, nil
	})}}
	_ = b.fetchTaildropFiles(context.Background(), staging, partial)
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal("could not inspect synthetic staging")
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(staging, entry.Name()))
		if err == nil && string(data) == original {
			return
		}
	}
	if daemon != replacement {
		t.Fatal("synthetic replacement was not delivered")
	}
	t.Fatal("original payload lost: daemon contains only the replacement, and original staging was withdrawn by basename")
}

func TestTaildropFinalNameHiddenUntilValidated(t *testing.T) {
	staging, directory := t.TempDir(), t.TempDir()
	const name = "synthetic.txt"
	if err := os.WriteFile(filepath.Join(staging, name), []byte("complete synthetic payload"), 0600); err != nil {
		t.Fatal("could not prepare synthetic source")
	}
	visible := false
	normalize := func(string) error {
		_, err := os.Stat(filepath.Join(directory, name))
		visible = err == nil
		return errors.New("synthetic owner/label validation failure")
	}
	_, _ = publishTaildropStagingWith(staging, directory, normalize)
	if visible {
		t.Fatal("final shared-storage filename was visible before owner/label validation succeeded")
	}
}

func TestTaildropAtomicPublicationPreservesConcurrentConflicts(t *testing.T) {
	staging, directory := t.TempDir(), t.TempDir()
	const name = "received.txt"
	if err := os.WriteFile(filepath.Join(staging, name), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("existing"), 0640); err != nil {
		t.Fatal(err)
	}
	normalize := func(path string) error {
		if !taildropTemporaryName.MatchString(filepath.Base(path)) {
			return errors.New("normalization did not use a hidden temporary file")
		}
		if _, err := os.Stat(filepath.Join(directory, "received (1).txt")); !os.IsNotExist(err) {
			return errors.New("final filename was visible before validation")
		}
		// A different writer wins the first candidate while we validate.
		return os.WriteFile(filepath.Join(directory, "received (1).txt"), []byte("concurrent"), 0640)
	}
	if n, err := publishTaildropStagingWith(staging, directory, normalize); err != nil || n != 1 {
		t.Fatalf("publish=%d err=%v", n, err)
	}
	for path, want := range map[string]string{name: "existing", "received (1).txt": "concurrent", "received (2).txt": "payload"} {
		if data, err := os.ReadFile(filepath.Join(directory, path)); err != nil || string(data) != want {
			t.Fatal("a concurrent file was overwritten or payload changed")
		}
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 3 {
		t.Fatal("temporary publication entry remained")
	}
	if _, err := os.Stat(taildropPublicationPath(staging)); !os.IsNotExist(err) {
		t.Fatal("publication journal remained")
	}
}

func TestTaildropPublicationRecoveryUsesExactIdentity(t *testing.T) {
	staging, directory := t.TempDir(), t.TempDir()
	temporary, file, err := createTaildropPublication(staging, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("interrupted"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	unrelated := filepath.Join(directory, ".shellscaled-taildrop-ffffffffffffffffffffffffffffffff.partial")
	if err := os.WriteFile(unrelated, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("recorded temporary file survived recovery")
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "foreign" {
		t.Fatal("unrecorded prefix-matching file was touched")
	}

	temporary, file, err = createTaildropPublication(staging, directory)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	parked := temporary + ".saved"
	if err := os.Rename(temporary, parked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err == nil {
		t.Fatal("replacement inode was treated as module-owned")
	}
	if data, err := os.ReadFile(temporary); err != nil || string(data) != "replacement" {
		t.Fatal("replacement was deleted")
	}
	_ = os.Remove(temporary)
	if err := os.Rename(parked, temporary); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err != nil {
		t.Fatal(err)
	}
}

func TestTaildropPublicationRenameFailuresKeepStaging(t *testing.T) {
	old := renameTaildropFile
	defer func() { renameTaildropFile = old }()
	for _, failure := range []error{syscall.EXDEV, syscall.ENOSYS, syscall.EOPNOTSUPP} {
		t.Run(failure.Error(), func(t *testing.T) {
			staging, directory := t.TempDir(), t.TempDir()
			source := filepath.Join(staging, "received.txt")
			if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			renameTaildropFile = func(string, string) error { return failure }
			if n, err := publishTaildropStagingWith(staging, directory, func(string) error { return nil }); err == nil || n != 0 {
				t.Fatal("unsupported atomic publication succeeded")
			}
			if data, err := os.ReadFile(source); err != nil || string(data) != "payload" {
				t.Fatal("atomic rename failure lost staging")
			}
			if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
				t.Fatal("failed publication exposed a destination")
			}
		})
	}
}

func TestTaildropPublicationRejectsReplacementAfterValidation(t *testing.T) {
	staging, directory := t.TempDir(), t.TempDir()
	source := filepath.Join(staging, "received.txt")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	normalize := func(temporary string) error {
		if err := os.Rename(temporary, temporary+".saved"); err != nil {
			return err
		}
		return os.WriteFile(temporary, []byte("foreign replacement"), 0600)
	}
	if n, err := publishTaildropStagingWith(staging, directory, normalize); err == nil || n != 0 {
		t.Fatal("replacement was published as the downloaded file")
	}
	if _, err := os.Stat(filepath.Join(directory, "received.txt")); !os.IsNotExist(err) {
		t.Fatal("replacement became visible at the final name")
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "payload" {
		t.Fatal("replacement lost the original staged content")
	}
}

func TestTaildropPublicationDirectoryReplacementIsPreserved(t *testing.T) {
	staging, directory := t.TempDir(), t.TempDir()
	temporary, file, err := createTaildropPublication(staging, directory)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	moved := directory + ".saved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(directory, filepath.Base(temporary))
	if err := os.WriteFile(foreign, []byte("foreign directory entry"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err == nil {
		t.Fatal("replacement directory was treated as module-owned")
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "foreign directory entry" {
		t.Fatal("replacement directory entry was removed")
	}
	replacement := directory + ".replacement"
	if err := os.Rename(directory, replacement); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(replacement)
	if err := os.Rename(moved, directory); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err != nil {
		t.Fatal(err)
	}
}

func TestTaildropUnverifiedIntentNeverDeletesPublicEntry(t *testing.T) {
	staging, directory := t.TempDir(), t.TempDir()
	temporary, file, err := createTaildropPublication(staging, directory)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	data, err := os.ReadFile(taildropPublicationPath(staging))
	if err != nil {
		t.Fatal(err)
	}
	var record taildropPublication
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	// Model a crash after exclusive creation but before the inode was saved.
	record.File = taildropFileIdentity{}
	if err := saveTaildropPublication(staging, record); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTaildropPublication(staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); err != nil {
		t.Fatal("an unverified public entry was deleted")
	}
	if _, err := os.Stat(taildropPublicationPath(staging)); !os.IsNotExist(err) {
		t.Fatal("unverified intent prevented a retry")
	}
}
