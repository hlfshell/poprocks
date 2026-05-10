package vm

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnvironmentVariablesRoundTripFile(t *testing.T) {
	ev := NewEnvironmentVariables()
	defer ev.Destroy()

	if err := ev.Set("FOO", "bar"); err != nil {
		t.Fatal(err)
	}
	if err := ev.Set("HELLO", "world"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), ".env")
	if err := ev.SaveToFile(path); err != nil {
		t.Fatal(err)
	}

	ev2, err := FromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ev2.Destroy()

	if got, ok := ev2.get("FOO"); !ok || got != "bar" {
		t.Fatalf("Get(FOO) = %q, %v", got, ok)
	}
	if got, ok := ev2.get("HELLO"); !ok || got != "world" {
		t.Fatalf("Get(HELLO) = %q, %v", got, ok)
	}
}

func TestEnvironmentVariablesStartsEmpty(t *testing.T) {
	ev := NewEnvironmentVariables()
	defer ev.Destroy()

	if got := ev.Len(); got != 0 {
		t.Fatalf("Len() = %d", got)
	}
}

func TestLoadEnvironmentVariablesFromFileMissingPath(t *testing.T) {
	_, err := FromFile(filepath.Join(t.TempDir(), ".env"))
	if !os.IsNotExist(err) {
		t.Fatalf("got %v", err)
	}
}

func TestEnvironmentVariablesKeys(t *testing.T) {
	ev := NewEnvironmentVariables()
	defer ev.Destroy()
	_ = ev.Set("Z", "1")
	_ = ev.Set("A", "2")

	if got := ev.Keys(); !reflect.DeepEqual(got, []string{"A", "Z"}) {
		t.Fatalf("Keys() = %v", got)
	}
}

func TestEnvironmentVariablesDelete(t *testing.T) {
	ev := NewEnvironmentVariables()
	defer ev.Destroy()
	_ = ev.Set("FOO", "bar")

	ev.Delete("FOO")

	if _, ok := ev.get("FOO"); ok {
		t.Fatal("expected value to be deleted")
	}
}

func TestEnvironmentVariablesSetAcceptsRawNames(t *testing.T) {
	ev := NewEnvironmentVariables()
	defer ev.Destroy()

	for _, name := range []string{
		"",
		"BAD=NAME",
		"BAD\x00NAME",
		"BAD NAME",
		"BAD\nNAME",
		"BAD\tNAME",
		"-BAD",
		"123BAD",
		"x=y",
	} {
		if err := ev.Set(name, "x"); err != nil {
			t.Fatalf("unexpected error for name %q: %v", name, err)
		}
		if got, ok := ev.get(name); !ok || got != "x" {
			t.Fatalf("get(%q) = %q, %v", name, got, ok)
		}
	}
}

func TestFromFileRejectsInvalidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("NOT_AN_ENV_LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := FromFile(path); !errors.Is(err, ErrEnvInvalidFile) {
		t.Fatalf("expected ErrEnvInvalidFile, got %v", err)
	}
}

func TestFromFileAllowsRawNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	payload := []byte("GOOD=ok\nBAD NAME=x\n")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	ev, err := FromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Destroy()

	if got, ok := ev.get("GOOD"); !ok || got != "ok" {
		t.Fatalf("get(GOOD) = %q, %v", got, ok)
	}
	if got, ok := ev.get("BAD NAME"); !ok || got != "x" {
		t.Fatalf("get(BAD NAME) = %q, %v", got, ok)
	}
}
