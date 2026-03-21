package drive

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func createTestDiskImage(t *testing.T, sizeMB int) string {
	t.Helper()

	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "test-disk.img")

	if _, err := exec.LookPath("qemu-img"); err == nil {
		cmd := exec.Command("qemu-img", "create", "-f", "raw", imagePath, fmt.Sprintf("%dM", sizeMB))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to create disk image with qemu-img: %v, output: %s", err, string(output))
		}
	} else {
		cmd := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMB), imagePath)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to create disk image with truncate: %v, output: %s", err, string(output))
		}
	}

	if _, err := exec.LookPath("mkfs.ext4"); err == nil {
		cmd := exec.Command("mkfs.ext4", "-F", imagePath)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cannot format disk image: %v, output: %s", err, string(output))
		}
	} else {
		t.Fatalf("mkfs.ext4 not available")
	}

	return imagePath
}

func createTestDrive(t *testing.T, imagePath string, readonly bool, filesystemType string) *BaseDrive {
	t.Helper()
	if filesystemType == "" {
		filesystemType = "ext4"
	}
	params := NewDriveOptionsPtr(
		"test-drive",
		readonly,
		false,
		"",
		"",
		false,
		10*1024*1024,
		filesystemType,
		MountMethodGuestMount,
	)
	return &BaseDrive{
		params:    params,
		imagePath: imagePath,
	}
}

func TestWriteToDrive_InvalidDrive(t *testing.T) {
	var drive *BaseDrive
	err := drive.WriteToDrive("/test/file.txt", bytes.NewReader([]byte("test")), 0o644)
	if !errors.Is(err, ErrDriveNotValid) {
		t.Errorf("expected ErrDriveNotValid, got: %v", err)
	}
}

func TestLoadFromDrive_InvalidDrive(t *testing.T) {
	var drive *BaseDrive
	_, err := drive.LoadFromDrive("/test/file.txt")
	if !errors.Is(err, ErrDriveNotValid) {
		t.Errorf("expected ErrDriveNotValid, got: %v", err)
	}
}

func TestWriteToDrive_RejectsSymlinkPath(t *testing.T) {
	mountPoint := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatalf("failed to create outside file: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(mountPoint, "safe"), 0o755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}
	linkPath := filepath.Join(mountPoint, "safe", "link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	drive := &BaseDrive{
		params:     NewDriveOptionsPtr("test-drive", false, false, "", "", false, 1024*1024, "ext4", MountMethodGuestMount),
		mounted:    true,
		mountPoint: mountPoint,
	}

	err := drive.WriteToDrive("/safe/link", bytes.NewReader([]byte("payload")), 0o644)
	if err == nil {
		t.Fatalf("expected symlink validation error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("expected symlink error, got: %v", err)
	}
}

func TestBaseDriveParameters_ReturnsCopy(t *testing.T) {
	drive := &BaseDrive{
		params:    NewDriveOptionsPtr("drive-1", false, false, "", "", false, 1024*1024, "ext4", MountMethodGuestMount),
		imagePath: "/tmp/example.img",
	}

	params := drive.Parameters()
	params.id = "mutated"
	params.readonly = true

	current := drive.Parameters()
	if current.ID() != "drive-1" {
		t.Fatalf("expected original id to remain unchanged, got: %s", current.ID())
	}
	if current.ReadOnly() {
		t.Fatalf("expected original readonly flag to remain unchanged")
	}
}

func TestBaseDriveFromImage_AutoDetectExt4(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "rootfs.ext4")
	data := make([]byte, 4096)
	data[1080] = 0x53
	data[1081] = 0xEF
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatalf("failed to create ext4-like image: %v", err)
	}

	opts := NewDriveOptionsPtr("rootfs", false, false, "", "", false, int64(len(data)), "ext4", MountMethodGuestMount)
	drive, err := BaseDriveFromImage(imagePath, opts)
	if err != nil {
		t.Fatalf("expected successful load, got: %v", err)
	}
	if drive == nil {
		t.Fatal("expected non-nil drive")
	}
	params := drive.Parameters()
	if params.FSType() != "ext4" {
		t.Fatalf("expected fsType ext4, got: %s", params.FSType())
	}
	if params.ReadOnly() {
		t.Fatal("expected ext4 autodetect to be writable by default")
	}
	if params.ID() != "rootfs" {
		t.Fatalf("expected derived id rootfs, got: %s", params.ID())
	}
}

func TestBaseDriveFromImage_AutoDetectSquashfs(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "readonly.squashfs")
	data := make([]byte, 4096)
	copy(data[:4], []byte("hsqs"))
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatalf("failed to create squashfs-like image: %v", err)
	}

	opts := NewDriveOptionsPtr("readonly", true, false, "", "", false, int64(len(data)), "squashfs", MountMethodGuestMount)
	drive, err := BaseDriveFromImage(imagePath, opts)
	if err != nil {
		t.Fatalf("expected successful load, got: %v", err)
	}
	params := drive.Parameters()
	if params.FSType() != "squashfs" {
		t.Fatalf("expected fsType squashfs, got: %s", params.FSType())
	}
	if !params.ReadOnly() {
		t.Fatal("expected squashfs autodetect to force readonly=true")
	}
}

func TestBaseDriveFromImage_RejectsUndetectedImage(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "random.bin")
	data := []byte("this is not a known filesystem image")
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatalf("failed to create random file: %v", err)
	}

	opts := NewDriveOptionsPtr("random", false, false, "", "", false, int64(len(data)), "ext4", MountMethodGuestMount)
	_, err := BaseDriveFromImage(imagePath, opts)
	if err == nil {
		t.Fatal("expected error for unsupported image")
	}
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("expected ErrInvalidParameter, got: %v", err)
	}
}

func TestBaseDriveFromImage_RejectsOptionMismatch(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "ro.squashfs")
	data := make([]byte, 4096)
	copy(data[:4], []byte("hsqs"))
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatalf("failed to create squashfs-like image: %v", err)
	}

	opts := NewDriveOptionsPtr("drive-1", false, false, "", "", false, int64(len(data)), "ext4", MountMethodGuestMount)
	_, err := BaseDriveFromImage(imagePath, opts)
	if err == nil {
		t.Fatal("expected mismatch error for provided options")
	}
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("expected ErrInvalidParameter, got: %v", err)
	}
}
