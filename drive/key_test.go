package drive

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestInMemoryKeyLifecycle(t *testing.T) {
	key := NewInMemoryKey([]byte("secret"))
	var got string
	if err := key.WithReader(func(r io.Reader) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		got = string(data)
		return nil
	}); err != nil {
		t.Fatalf("expected key read success, got: %v", err)
	}
	if got != "secret" {
		t.Fatalf("unexpected key data: %q", got)
	}

	key.Destroy()
	if err := key.WithReader(func(_ io.Reader) error { return nil }); err == nil {
		t.Fatal("expected key unavailable after destroy")
	}
}

func TestFileKey(t *testing.T) {
	path := t.TempDir() + "/key.bin"
	if err := os.WriteFile(path, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}
	key := &FileKey{Path: path}
	if err := key.WithReader(func(r io.Reader) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if string(data) != "file-secret" {
			t.Fatalf("unexpected file key content: %q", string(data))
		}
		return nil
	}); err != nil {
		t.Fatalf("expected file key read success, got: %v", err)
	}
}

func TestCommandKey(t *testing.T) {
	key := &CommandKey{Name: "sh", Args: []string{"-c", "printf cmd-secret"}}
	if err := key.WithReader(func(r io.Reader) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if string(data) != "cmd-secret" {
			t.Fatalf("unexpected command key content: %q", string(data))
		}
		return nil
	}); err != nil {
		t.Fatalf("expected command key read success, got: %v", err)
	}
}

func TestOneShotKey(t *testing.T) {
	key := NewOneShotKey(NewInMemoryKey([]byte("once")))
	if err := key.WithReader(func(r io.Reader) error {
		_, err := io.ReadAll(r)
		return err
	}); err != nil {
		t.Fatalf("expected first one-shot use to succeed, got: %v", err)
	}
	err := key.WithReader(func(_ io.Reader) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "one-shot") {
		t.Fatalf("expected one-shot error on second use, got: %v", err)
	}
}
