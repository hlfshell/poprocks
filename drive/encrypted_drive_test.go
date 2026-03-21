package drive

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testKey struct {
	data         []byte
	withReader   int
	destroyCalls int
}

func (k *testKey) WithReader(fn func(r io.Reader) error) error {
	k.withReader++
	return fn(bytes.NewReader(k.data))
}

func (k *testKey) Destroy() {
	k.destroyCalls++
}

func TestEncryptedDriveFromImageValidationAndDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "enc.img")
	if err := os.WriteFile(imagePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("failed to write test image: %v", err)
	}

	if _, err := EncryptedDriveFromImage("", &testKey{data: []byte("k")}, nil); err == nil {
		t.Fatal("expected error for empty image path")
	}
	if _, err := EncryptedDriveFromImage(imagePath, nil, nil); err == nil {
		t.Fatal("expected error for nil key")
	}

	_, err := EncryptedDriveFromImage(
		imagePath,
		&testKey{data: []byte("k")},
		NewDriveOptionsPtr("bad-fs", false, false, "", "", false, 4096, "xfs", MountMethodLoop),
	)
	if err == nil || !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("expected ErrInvalidParameter for non-ext4 fs, got: %v", err)
	}

	d, err := EncryptedDriveFromImage(imagePath, &testKey{data: []byte("k")}, nil)
	if err != nil {
		t.Fatalf("expected successful encrypted drive creation, got: %v", err)
	}
	if !d.Validate() {
		t.Fatal("expected encrypted drive to validate")
	}
	params := d.Parameters()
	if params.FSType() != "ext4" {
		t.Fatalf("expected default fsType ext4, got: %s", params.FSType())
	}
	if params.MountMethod() != MountMethodLoop {
		t.Fatalf("expected default mount method loop, got: %s", params.MountMethod())
	}
}

func TestEncryptedDriveDeleteCleansStateAndDestroysKey(t *testing.T) {
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "ephemeral.img")
	if err := os.WriteFile(imagePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create image: %v", err)
	}

	key := &testKey{data: []byte("k")}
	d := &EncryptedDrive{
		params:    NewDriveOptionsPtr("enc-id", false, false, "", "", true, 4096, "ext4", MountMethodLoop),
		imagePath: imagePath,
		key:       key,
	}

	if err := d.Delete(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if _, err := os.Stat(imagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ephemeral encrypted image removed, got: %v", err)
	}
	if d.params != nil || d.imagePath != "" || d.key != nil {
		t.Fatal("expected encrypted drive internals cleared after delete")
	}
	if key.destroyCalls != 1 {
		t.Fatalf("expected key destroy called once, got: %d", key.destroyCalls)
	}
}

func TestEnsureEncryptionToolingMissingAndPresent(t *testing.T) {
	t.Run("missing_tools", func(t *testing.T) {
		emptyDir := t.TempDir()
		t.Setenv("PATH", emptyDir)

		err := ensureEncryptionTooling()
		if err == nil {
			t.Fatal("expected missing tooling error")
		}
		if !errors.Is(err, ErrEncryptionUnavailable) {
			t.Fatalf("expected ErrEncryptionUnavailable, got: %v", err)
		}
	})

	t.Run("all_tools_present", func(t *testing.T) {
		binDir := t.TempDir()
		writeExec(t, filepath.Join(binDir, "cryptsetup"), "#!/bin/sh\nexit 0\n")
		writeExec(t, filepath.Join(binDir, "truncate"), "#!/bin/sh\nexit 0\n")
		writeExec(t, filepath.Join(binDir, "mkfs.ext4"), "#!/bin/sh\nexit 0\n")
		writeExec(t, filepath.Join(binDir, "mount"), "#!/bin/sh\nexit 0\n")
		writeExec(t, filepath.Join(binDir, "umount"), "#!/bin/sh\nexit 0\n")
		t.Setenv("PATH", binDir)

		if err := ensureEncryptionTooling(); err != nil {
			t.Fatalf("expected tooling check success, got: %v", err)
		}
	})
}

func TestEncryptedDriveRunCryptsetupWithKeyUsesStdin(t *testing.T) {
	binDir := t.TempDir()
	argsLog := filepath.Join(t.TempDir(), "args.log")
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$CRYPTSETUP_ARGS_LOG\"\n" +
		"/bin/cat > \"$CRYPTSETUP_STDIN_LOG\"\n"
	writeExec(t, filepath.Join(binDir, "cryptsetup"), script)
	t.Setenv("PATH", binDir)
	t.Setenv("CRYPTSETUP_ARGS_LOG", argsLog)
	t.Setenv("CRYPTSETUP_STDIN_LOG", stdinLog)

	key := &testKey{data: []byte("super-secret")}
	d := &EncryptedDrive{
		params:    NewDriveOptionsPtr("enc-id", false, false, "", "", false, 4096, "ext4", MountMethodLoop),
		imagePath: "/tmp/test.img",
		key:       key,
	}

	if err := d.runCryptsetupWithKey("luksOpen", "/tmp/test.img", "map-1", "--key-file", "-"); err != nil {
		t.Fatalf("expected cryptsetup execution to succeed, got: %v", err)
	}
	if key.withReader != 1 {
		t.Fatalf("expected key WithReader called once, got: %d", key.withReader)
	}

	argsBytes, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("failed to read args log: %v", err)
	}
	argsText := string(argsBytes)
	if !strings.Contains(argsText, "luksOpen") || !strings.Contains(argsText, "--key-file") {
		t.Fatalf("unexpected cryptsetup args log: %q", argsText)
	}

	stdinBytes, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("failed to read stdin log: %v", err)
	}
	if string(stdinBytes) != "super-secret" {
		t.Fatalf("expected key material in stdin, got: %q", string(stdinBytes))
	}
}

func TestEncryptedDriveEnsureLUKSImage(t *testing.T) {
	t.Run("existing_non_luks_image_fails", func(t *testing.T) {
		binDir := t.TempDir()
		writeExec(t, filepath.Join(binDir, "cryptsetup"), "#!/bin/sh\nexit 1\n")
		t.Setenv("PATH", binDir)

		imagePath := filepath.Join(t.TempDir(), "existing.img")
		if err := os.WriteFile(imagePath, []byte("existing"), 0o644); err != nil {
			t.Fatalf("failed to create image: %v", err)
		}

		d := &EncryptedDrive{
			params:    NewDriveOptionsPtr("enc-id", false, false, "", "", false, 4096, "ext4", MountMethodLoop),
			imagePath: imagePath,
			key:       &testKey{data: []byte("k")},
		}
		err := d.ensureLUKSImage()
		if err == nil {
			t.Fatal("expected existing non-luks image error")
		}
		if !errors.Is(err, ErrInvalidParameter) {
			t.Fatalf("expected ErrInvalidParameter, got: %v", err)
		}
	})

	t.Run("creates_new_luks_image_when_missing", func(t *testing.T) {
		binDir := t.TempDir()
		scriptLog := filepath.Join(t.TempDir(), "script.log")
		truncateScript := "#!/bin/sh\n" +
			"printf 'truncate %s\\n' \"$*\" >> \"$ENCRYPTED_TEST_LOG\"\n" +
			": > \"$3\"\n"
		cryptsetupScript := "#!/bin/sh\n" +
			"printf 'cryptsetup %s\\n' \"$*\" >> \"$ENCRYPTED_TEST_LOG\"\n" +
			"/bin/cat >/dev/null\n"
		mkfsScript := "#!/bin/sh\n" +
			"printf 'mkfs %s\\n' \"$*\" >> \"$ENCRYPTED_TEST_LOG\"\n" +
			"exit 0\n"
		writeExec(t, filepath.Join(binDir, "truncate"), truncateScript)
		writeExec(t, filepath.Join(binDir, "cryptsetup"), cryptsetupScript)
		writeExec(t, filepath.Join(binDir, "mkfs.ext4"), mkfsScript)
		t.Setenv("PATH", binDir)
		t.Setenv("ENCRYPTED_TEST_LOG", scriptLog)

		imagePath := filepath.Join(t.TempDir(), "missing.img")
		key := &testKey{data: []byte("super-secret")}
		d := &EncryptedDrive{
			params:    NewDriveOptionsPtr("enc-id", false, false, "", "", false, 8*1024*1024, "ext4", MountMethodLoop),
			imagePath: imagePath,
			key:       key,
		}

		if err := d.ensureLUKSImage(); err != nil {
			t.Fatalf("expected LUKS image creation to succeed, got: %v", err)
		}
		if _, err := os.Stat(imagePath); err != nil {
			t.Fatalf("expected created image to exist, stat err: %v", err)
		}
		if key.withReader != 2 {
			t.Fatalf("expected key used twice (format + open), got: %d", key.withReader)
		}

		logBytes, err := os.ReadFile(scriptLog)
		if err != nil {
			t.Fatalf("failed to read script log: %v", err)
		}
		logText := string(logBytes)
		if !strings.Contains(logText, "truncate -s") {
			t.Fatalf("expected truncate invocation, got log: %q", logText)
		}
		if !strings.Contains(logText, "cryptsetup --batch-mode --type luks2 luksFormat") {
			t.Fatalf("expected luksFormat invocation, got log: %q", logText)
		}
		if !strings.Contains(logText, "cryptsetup luksOpen") {
			t.Fatalf("expected luksOpen invocation, got log: %q", logText)
		}
		if !strings.Contains(logText, "mkfs -F") {
			t.Fatalf("expected mkfs invocation, got log: %q", logText)
		}
		if !strings.Contains(logText, "cryptsetup luksClose") {
			t.Fatalf("expected deferred luksClose invocation, got log: %q", logText)
		}
	})
}
