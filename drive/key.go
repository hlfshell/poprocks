package drive

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Key encapsulates key material delivery to cryptsetup.
// Callers should invoke Destroy when a key is no longer needed.
type Key interface {
	WithReader(fn func(r io.Reader) error) error
	Destroy()
}

// InMemoryKey keeps key bytes in memory and zeroes them on Destroy.
type InMemoryKey struct {
	lock sync.Mutex
	data []byte
}

func NewInMemoryKey(key []byte) *InMemoryKey {
	cp := make([]byte, len(key))
	copy(cp, key)
	return &InMemoryKey{data: cp}
}

func (k *InMemoryKey) WithReader(fn func(r io.Reader) error) error {
	k.lock.Lock()
	defer k.lock.Unlock()
	if len(k.data) == 0 {
		return fmt.Errorf("%w: key material unavailable", ErrInvalidParameter)
	}
	return fn(bytes.NewReader(k.data))
}

func (k *InMemoryKey) Destroy() {
	k.lock.Lock()
	defer k.lock.Unlock()
	for i := range k.data {
		k.data[i] = 0
	}
	k.data = nil
}

// ReaderKey streams key material on demand (e.g. from secret manager or pipe).
type ReaderKey struct {
	Open func() (io.ReadCloser, error)
}

func (k *ReaderKey) WithReader(fn func(r io.Reader) error) error {
	if k == nil || k.Open == nil {
		return fmt.Errorf("%w: key reader unavailable", ErrInvalidParameter)
	}
	rc, err := k.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return fn(rc)
}

func (k *ReaderKey) Destroy() {}

// FileKey reads key material from a file at read-time.
type FileKey struct {
	Path string
}

func (k *FileKey) WithReader(fn func(r io.Reader) error) error {
	if k == nil || k.Path == "" {
		return fmt.Errorf("%w: key file path is required", ErrInvalidParameter)
	}
	file, err := os.Open(k.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	return fn(file)
}

func (k *FileKey) Destroy() {}

// CommandKey executes a command and streams stdout as key material.
type CommandKey struct {
	Name string
	Args []string
}

func (k *CommandKey) WithReader(fn func(r io.Reader) error) error {
	if k == nil || k.Name == "" {
		return fmt.Errorf("%w: key command is required", ErrInvalidParameter)
	}
	cmd := exec.Command(k.Name, k.Args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	fnErr := fn(stdout)
	waitErr := cmd.Wait()
	if fnErr != nil {
		return fnErr
	}
	if waitErr != nil {
		return waitErr
	}
	return nil
}

func (k *CommandKey) Destroy() {}

// OneShotKey allows using an underlying key exactly once, then destroys it.
type OneShotKey struct {
	lock sync.Mutex
	used bool
	key  Key
}

func NewOneShotKey(key Key) *OneShotKey {
	return &OneShotKey{key: key}
}

func (k *OneShotKey) WithReader(fn func(r io.Reader) error) error {
	k.lock.Lock()
	if k.used {
		k.lock.Unlock()
		return fmt.Errorf("%w: one-shot key already consumed", ErrInvalidParameter)
	}
	if k.key == nil {
		k.lock.Unlock()
		return fmt.Errorf("%w: one-shot key is nil", ErrInvalidParameter)
	}
	k.used = true
	inner := k.key
	k.lock.Unlock()

	err := inner.WithReader(fn)
	inner.Destroy()
	k.lock.Lock()
	k.key = nil
	k.lock.Unlock()
	return err
}

func (k *OneShotKey) Destroy() {
	k.lock.Lock()
	defer k.lock.Unlock()
	if k.key != nil {
		k.key.Destroy()
	}
	k.key = nil
	k.used = true
}

// NewSHA256PassphraseKey is a convenience helper for passphrase users.
func NewSHA256PassphraseKey(passphrase []byte) (*InMemoryKey, error) {
	if len(passphrase) == 0 {
		return nil, fmt.Errorf("%w: passphrase cannot be empty", ErrInvalidParameter)
	}
	sum := sha256.Sum256(passphrase)
	key := NewInMemoryKey(sum[:])
	for i := range passphrase {
		passphrase[i] = 0
	}
	return key, nil
}
