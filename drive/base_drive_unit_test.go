package drive

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseDriveValidateAndGetters(t *testing.T) {
	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		imagePath: "/tmp/image.img",
	}

	if !d.Validate() {
		t.Fatal("expected valid drive")
	}
	if d.ImagePath() != "/tmp/image.img" {
		t.Fatalf("unexpected image path: %s", d.ImagePath())
	}
	if d.Mounted() {
		t.Fatal("expected drive to start unmounted")
	}
}

func TestBaseDriveUnmountNotMounted(t *testing.T) {
	d := &BaseDrive{
		params: NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
	}
	err := d.Unmount()
	if !errors.Is(err, ErrDriveNotMounted) {
		t.Fatalf("expected ErrDriveNotMounted, got: %v", err)
	}
}

func TestBaseDriveUnmountNoUnmountFunc(t *testing.T) {
	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		mounted:   true,
		imagePath: "/tmp/image.img",
	}
	err := d.Unmount()
	if err == nil || !strings.Contains(err.Error(), "no unmount function") {
		t.Fatalf("expected missing unmount function error, got: %v", err)
	}
}

func TestBaseDriveUnmountFailureResetsState(t *testing.T) {
	d := &BaseDrive{
		params:     NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		mounted:    true,
		mountPoint: t.TempDir(),
		unmountFunc: func() error {
			return errors.New("boom")
		},
	}

	err := d.Unmount()
	if err == nil || !strings.Contains(err.Error(), "failed to unmount drive") {
		t.Fatalf("expected unmount failure, got: %v", err)
	}
	if d.unMounting {
		t.Fatal("expected unmounting flag reset on unmount failure")
	}
}

func TestBaseDriveUnmountEphemeralRemovesMountDir(t *testing.T) {
	mountDir := t.TempDir()
	params := NewDriveOptionsPtr("id-1", false, false, "", "", true, 4096, "ext4", MountMethodGuestMount)
	d := &BaseDrive{
		params:      params,
		mounted:     true,
		mountPoint:  mountDir,
		unmountFunc: func() error { return nil },
	}

	if err := d.Unmount(); err != nil {
		t.Fatalf("unexpected unmount error: %v", err)
	}
	if _, err := os.Stat(mountDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected mount dir removed, got err=%v", err)
	}
	if d.Mounted() {
		t.Fatal("expected drive to be unmounted")
	}
}

func TestBaseDriveDeleteEphemeralRemovesImageAndClearsState(t *testing.T) {
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "ephemeral.img")
	if err := os.WriteFile(imagePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create image: %v", err)
	}

	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", true, 4096, "ext4", MountMethodGuestMount),
		imagePath: imagePath,
	}
	if err := d.Delete(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if _, err := os.Stat(imagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected image removed, got err=%v", err)
	}
	if d.params != nil || d.imagePath != "" {
		t.Fatal("expected drive internals cleared after delete")
	}
}

func TestBaseDriveWriteAndLoadFlowWithoutMountTooling(t *testing.T) {
	mountDir := t.TempDir()
	d := &BaseDrive{
		params:     NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		mounted:    true,
		mountPoint: mountDir,
	}

	payload := []byte("hello base drive")
	if err := d.WriteToDrive("/a/b/file.txt", bytes.NewReader(payload), 0o640); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	out, err := d.LoadFromDrive("/a/b/file.txt")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(out) != string(payload) {
		t.Fatalf("payload mismatch: expected %q got %q", payload, out)
	}
}

func TestBaseDriveWriteReadonlyAndSquashfsGuards(t *testing.T) {
	mountDir := t.TempDir()

	readonlyDrive := &BaseDrive{
		params:     NewDriveOptionsPtr("id-ro", true, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		mounted:    true,
		mountPoint: mountDir,
	}
	if err := readonlyDrive.WriteToDrive("/f.txt", bytes.NewReader([]byte("x")), 0o644); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected readonly error, got: %v", err)
	}

	squashDrive := &BaseDrive{
		params:     NewDriveOptionsPtr("id-sq", false, false, "", "", false, 4096, "squashfs", MountMethodGuestMount),
		mounted:    true,
		mountPoint: mountDir,
	}
	if err := squashDrive.WriteToDrive("/f.txt", bytes.NewReader([]byte("x")), 0o644); err == nil || !strings.Contains(err.Error(), "squashfs") {
		t.Fatalf("expected squashfs write guard error, got: %v", err)
	}
}

func TestBaseDriveLoadFromDriveNotFound(t *testing.T) {
	d := &BaseDrive{
		params:     NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		mounted:    true,
		mountPoint: t.TempDir(),
	}
	_, err := d.LoadFromDrive("/missing.txt")
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "failed to inspect path segment") {
		t.Fatalf("expected path inspection failure for missing file, got: %v", err)
	}
}

func TestBaseDriveMountAndUnmountViaGuestTools(t *testing.T) {
	binDir := t.TempDir()
	writeExec(t, filepath.Join(binDir, "guestmount"), "#!/bin/sh\nexit 0\n")
	writeExec(t, filepath.Join(binDir, "guestunmount"), "#!/bin/sh\nexit 0\n")

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+oldPath)

	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		imagePath: "/tmp/fake.img",
	}
	mountPoint := t.TempDir()
	if err := d.Mount(mountPoint); err != nil {
		t.Fatalf("expected guestmount path success, got: %v", err)
	}
	if !d.Mounted() {
		t.Fatal("expected drive mounted")
	}
	if err := d.Unmount(); err != nil {
		t.Fatalf("expected unmount success, got: %v", err)
	}
	if d.Mounted() {
		t.Fatal("expected drive unmounted")
	}
}

func TestBaseDriveMountGuards(t *testing.T) {
	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		imagePath: "/tmp/fake.img",
		mounted:   true,
	}
	if err := d.Mount(t.TempDir()); !errors.Is(err, ErrDriveAlreadyMounted) {
		t.Fatalf("expected ErrDriveAlreadyMounted, got: %v", err)
	}

	d = &BaseDrive{
		params:     NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		imagePath:  "/tmp/fake.img",
		unMounting: true,
	}
	if err := d.Mount(t.TempDir()); err == nil || !strings.Contains(err.Error(), "currently unmounting") {
		t.Fatalf("expected unmounting guard error, got: %v", err)
	}

	d = &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "ext4", MountMethodGuestMount),
		imagePath: "/tmp/fake.img",
	}
	if err := d.Mount(""); err == nil || !strings.Contains(err.Error(), "specified mount point is required") {
		t.Fatalf("expected mountpoint required error, got: %v", err)
	}

	d = &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", false, 4096, "squashfs", MountMethodGuestMount),
		imagePath: "/tmp/fake.img",
	}
	if err := d.Mount(t.TempDir()); err == nil || !strings.Contains(err.Error(), "squashfs drives must be read-only") {
		t.Fatalf("expected squashfs readonly guard error, got: %v", err)
	}
}

func TestBaseDriveMountGuestmountFailsWithoutFallback(t *testing.T) {
	binDir := t.TempDir()
	writeExec(t, filepath.Join(binDir, "guestmount"), "#!/bin/sh\nexit 1\n")
	writeExec(t, filepath.Join(binDir, "guestunmount"), "#!/bin/sh\nexit 0\n")

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+oldPath)

	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-1", false, false, "", "", true, 4096, "ext4", MountMethodGuestMount),
		imagePath: "/tmp/fake.img",
	}
	err := d.Mount("")
	if err == nil || !strings.Contains(err.Error(), "guestmount failed") {
		t.Fatalf("expected guestmount failure without fallback, got: %v", err)
	}
}

func TestBaseDriveMountLoopRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root user for root-requirement check")
	}

	d := &BaseDrive{
		params:    NewDriveOptionsPtr("id-loop", false, false, "", "", true, 4096, "ext4", MountMethodLoop),
		imagePath: "/tmp/fake.img",
	}
	err := d.Mount("")
	if err == nil || !strings.Contains(err.Error(), "loop mount requires root privileges") {
		t.Fatalf("expected loop root requirement error, got: %v", err)
	}
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("failed to write executable %s: %v", path, err)
	}
}
