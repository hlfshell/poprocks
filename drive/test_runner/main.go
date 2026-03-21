package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hlfshell/poprocks/drive"
)

func main() {
	scenario := os.Getenv("SCENARIO")
	fixtureDir := os.Getenv("FIXTURE_DIR")
	if fixtureDir == "" {
		fixtureDir = "/fixtures"
	}

	switch scenario {
	case "guestmount_nonroot_ext4_rw":
		must(runRWScenario(filepath.Join(fixtureDir, "ext4.img"), drive.MountMethodGuestMount))
	case "guestmount_auto_ext4_rw":
		must(runAutoRWScenario(filepath.Join(fixtureDir, "ext4.img")))
	case "auto_detect_none_available":
		must(runAutoDetectNoBackendScenario(filepath.Join(fixtureDir, "ext4.img")))
	case "root_loop_ext4_rw":
		must(runRWScenario(filepath.Join(fixtureDir, "ext4.img"), drive.MountMethodLoop))
	case "squashfs_readonly":
		must(runSquashfsReadonlyScenario(filepath.Join(fixtureDir, "squashfs.img")))
	case "delete_ephemeral":
		must(runDeleteEphemeralScenario(filepath.Join(fixtureDir, "ext4.img")))
	case "luks_dmcrypt_ext4_rw":
		must(runLUKSRWScenario())
	default:
		fail("unknown scenario: %s", scenario)
	}
}

func runRWScenario(srcImage string, method drive.MountMethod) error {
	imagePath, err := copyFixture(srcImage)
	if err != nil {
		return err
	}

	info, err := os.Stat(imagePath)
	if err != nil {
		return err
	}
	opts := drive.NewDriveOptionsPtr("scenario-rw", false, false, "", "", false, info.Size(), "ext4", method)
	d, err := drive.BaseDriveFromImage(imagePath, opts)
	if err != nil {
		return fmt.Errorf("load image failed: %w", err)
	}

	mountDir, err := os.MkdirTemp("", "drive-mnt-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	if err := d.Mount(mountDir); err != nil {
		return fmt.Errorf("mount failed: %w", err)
	}
	defer d.Unmount()

	want := []byte("scenario payload")
	if err := d.WriteToDrive("/scenario/file.txt", bytes.NewReader(want), 0o644); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	got, err := d.LoadFromDrive("/scenario/file.txt")
	if err != nil {
		return fmt.Errorf("read failed: %w", err)
	}
	if string(got) != string(want) {
		return fmt.Errorf("payload mismatch: expected %q got %q", string(want), string(got))
	}

	if err := d.Unmount(); err != nil {
		return fmt.Errorf("unmount failed: %w", err)
	}
	return nil
}

func runAutoRWScenario(srcImage string) error {
	imagePath, err := copyFixture(srcImage)
	if err != nil {
		return err
	}
	d, err := drive.BaseDriveFromImage(imagePath, nil)
	if err != nil {
		return fmt.Errorf("auto load image failed: %w", err)
	}
	method := d.Parameters().MountMethod()
	if method != drive.MountMethodGuestMount && method != drive.MountMethodLoop {
		return fmt.Errorf("unexpected detected mount method: %q", method)
	}

	mountDir, err := os.MkdirTemp("", "drive-auto-mnt-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	if err := d.Mount(mountDir); err != nil {
		return fmt.Errorf("auto mount failed: %w", err)
	}
	defer d.Unmount()

	want := []byte("auto scenario payload")
	if err := d.WriteToDrive("/scenario/auto.txt", bytes.NewReader(want), 0o644); err != nil {
		return fmt.Errorf("auto write failed: %w", err)
	}
	got, err := d.LoadFromDrive("/scenario/auto.txt")
	if err != nil {
		return fmt.Errorf("auto read failed: %w", err)
	}
	if string(got) != string(want) {
		return fmt.Errorf("auto payload mismatch: expected %q got %q", string(want), string(got))
	}
	return nil
}

func runAutoDetectNoBackendScenario(srcImage string) error {
	imagePath, err := copyFixture(srcImage)
	if err != nil {
		return err
	}

	originalPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", "/nonexistent"); err != nil {
		return err
	}
	defer os.Setenv("PATH", originalPath)

	_, err = drive.BaseDriveFromImage(imagePath, nil)
	if err == nil {
		return fmt.Errorf("expected mount autodetection failure with empty PATH")
	}
	if !strings.Contains(err.Error(), "no usable mount method available") {
		return fmt.Errorf("unexpected autodetect error: %v", err)
	}
	return nil
}

func runSquashfsReadonlyScenario(srcImage string) error {
	info, err := os.Stat(srcImage)
	if err != nil {
		return err
	}
	opts := drive.NewDriveOptionsPtr("scenario-squash", true, false, "", "", false, info.Size(), "squashfs", drive.MountMethodLoop)
	d, err := drive.BaseDriveFromImage(srcImage, opts)
	if err != nil {
		return fmt.Errorf("load squashfs failed: %w", err)
	}
	if !d.Parameters().ReadOnly() {
		return fmt.Errorf("expected squashfs to be readonly")
	}

	mountDir, err := os.MkdirTemp("", "drive-sq-mnt-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	if err := d.Mount(mountDir); err != nil {
		return fmt.Errorf("mount squashfs failed: %w", err)
	}
	defer d.Unmount()

	got, err := d.LoadFromDrive("/data/hello.txt")
	if err != nil {
		return fmt.Errorf("read squashfs file failed: %w", err)
	}
	if !strings.Contains(string(got), "hello from squashfs fixture") {
		return fmt.Errorf("unexpected squashfs content: %q", string(got))
	}
	if err := d.WriteToDrive("/data/new.txt", bytes.NewReader([]byte("x")), 0o644); err == nil {
		return fmt.Errorf("expected write to squashfs to fail")
	}
	return nil
}

func runDeleteEphemeralScenario(srcImage string) error {
	imagePath, err := copyFixture(srcImage)
	if err != nil {
		return err
	}

	info, err := os.Stat(imagePath)
	if err != nil {
		return err
	}
	opts := drive.NewDriveOptionsPtr("ephemeral", false, false, "", "", true, info.Size(), "ext4", drive.MountMethodLoop)
	d, err := drive.BaseDriveFromImage(imagePath, opts)
	if err != nil {
		return err
	}
	if err := d.Delete(); err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		return fmt.Errorf("expected ephemeral image removed, stat err=%v", err)
	}
	return nil
}

func runLUKSRWScenario() error {
	imagePath := filepath.Join(os.TempDir(), "drive-luks-scenario.img")
	_ = os.Remove(imagePath)

	opts := drive.NewDriveOptionsPtr("scenario-luks", false, false, "", "", true, 32*1024*1024, "ext4", drive.MountMethodLoop)
	key, err := drive.NewSHA256PassphraseKey([]byte("scenario-passphrase"))
	if err != nil {
		return fmt.Errorf("create key failed: %w", err)
	}
	d, err := drive.EncryptedDriveFromImage(imagePath, key, opts)
	if err != nil {
		return fmt.Errorf("create encrypted drive failed: %w", err)
	}

	mountDir, err := os.MkdirTemp("", "drive-luks-mnt-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	if err := d.Mount(mountDir); err != nil {
		return fmt.Errorf("mount encrypted drive failed: %w", err)
	}
	defer d.Unmount()

	want := []byte("luks scenario payload")
	if err := d.WriteToDrive("/secure/data.txt", bytes.NewReader(want), 0o600); err != nil {
		return fmt.Errorf("write encrypted drive failed: %w", err)
	}
	got, err := d.LoadFromDrive("/secure/data.txt")
	if err != nil {
		return fmt.Errorf("read encrypted drive failed: %w", err)
	}
	if string(got) != string(want) {
		return fmt.Errorf("encrypted payload mismatch: expected %q got %q", string(want), string(got))
	}

	if err := d.Unmount(); err != nil {
		return fmt.Errorf("unmount encrypted drive failed: %w", err)
	}
	if err := d.Delete(); err != nil {
		return fmt.Errorf("delete encrypted drive failed: %w", err)
	}
	if _, err := os.Stat(imagePath); !os.IsNotExist(err) {
		return fmt.Errorf("expected encrypted ephemeral image removed, stat err=%v", err)
	}
	return nil
}

func copyFixture(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	dst := filepath.Join(os.TempDir(), filepath.Base(src)+"-copy.img")
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	return dst, nil
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
