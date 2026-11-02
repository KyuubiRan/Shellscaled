package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// A new generation is written for every request, including off. Absence is the
// original default, not an acknowledgement of a later off request. This also
// prevents an old off acknowledgement from matching an on/off/on/off sequence.
type magicDNSRequest struct {
	Enabled    bool   `json:"enabled"`
	Generation string `json:"generation"`
}

var errMagicDNSPlanSuperseded = errors.New("MagicDNS request superseded")

func readMagicDNSRequest() (magicDNSRequest, error) {
	file, err := os.Open(magicDNSEnabledFile)
	if errors.Is(err, os.ErrNotExist) {
		return magicDNSRequest{}, nil
	}
	if err != nil {
		return magicDNSRequest{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(data) > 256 {
		return magicDNSRequest{}, errors.New("invalid MagicDNS preference")
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		// The original opt-in file was an empty file or "1\n". Keep those
		// installations enabled until the next explicit request replaces it.
		sum := sha256.Sum256(data)
		return magicDNSRequest{true, "legacy-" + hex.EncodeToString(sum[:])}, nil
	}
	var request magicDNSRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return magicDNSRequest{}, errors.New("invalid MagicDNS preference")
	}
	decoded, err := hex.DecodeString(request.Generation)
	if err != nil || len(decoded) != 16 {
		return magicDNSRequest{}, errors.New("invalid MagicDNS generation")
	}
	return request, nil
}

// Only the proxy changes chain contents. This lock serializes its short apply
// phase with preference writes, never with LocalAPI or DoH network probes.
func withMagicDNSControl(ctx context.Context, apply func() error) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(magicDNSEnabledFile), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(magicDNSEnabledFile+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("invalid MagicDNS lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return apply()
}

func requestMagicDNS(ctx context.Context, enabled bool) error {
	return withMagicDNSControl(ctx, func() error {
		var generation [16]byte
		if _, err := rand.Read(generation[:]); err != nil {
			return err
		}
		data, err := json.Marshal(magicDNSRequest{enabled, hex.EncodeToString(generation[:])})
		if err != nil {
			return err
		}
		return atomicMetadata(magicDNSEnabledFile, append(data, '\n'))
	})
}
