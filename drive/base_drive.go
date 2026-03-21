package drive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// BaseDrive describes a Firecracker block device backed by a disk image.
type BaseDrive struct {
	params *DriveOptions

	imagePath string

	mounted     bool
	mountPoint  string
	unmountFunc func() error

	ioWG       sync.WaitGroup
	unMounting bool

	lock sync.Mutex
}

func (d *BaseDrive) Validate() bool {
	if d == nil {
		return false
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.params == nil || !d.params.Validate() {
		return false
	}
	return true
}

func (d *BaseDrive) Parameters() *DriveOptions {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.params.Clone()
}

func (d *BaseDrive) Mounted() bool {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.mounted
}

func (d *BaseDrive) ImagePath() string {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.imagePath
}

func (d *BaseDrive) Mount(mountPoint string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	if d.mounted {
		return ErrDriveAlreadyMounted
	}
	if d.unMounting {
		return fmt.Errorf("drive is currently unmounting")
	}
	if mountPoint == "" && !d.params.Ephemeral() {
		return fmt.Errorf("a specified mount point is required for non-ephemeral drives")
	}

	fsType := d.params.FSType()
	readonly := d.params.ReadOnly()
	imagePath := d.imagePath
	if fsType == "squashfs" && !readonly {
		return fmt.Errorf("squashfs drives must be read-only")
	}

	mountOpts := "nosuid,nodev,noexec"
	if readonly {
		mountOpts += ",ro"
	}

	if mountPoint == "" {
		var err error
		mountPoint, err = os.MkdirTemp("", "drive-mount-")
		if err != nil {
			return fmt.Errorf("failed to create mount point: %w", err)
		}
	}

	switch d.params.MountMethod() {
	case MountMethodGuestMount:
		return d.mountWithGuestMount(imagePath, mountPoint, readonly)
	case MountMethodLoop:
		return d.mountWithLoopDevice(imagePath, mountPoint, fsType, mountOpts)
	default:
		return fmt.Errorf("unsupported mount method: %s", d.params.MountMethod())
	}
}

func (d *BaseDrive) mountWithGuestMount(imagePath, mountPoint string, readonly bool) error {
	guestMountExec, err := exec.LookPath("guestmount")
	if err != nil || guestMountExec == "" {
		return fmt.Errorf("guestmount not available")
	}
	guestUMountExec, err := exec.LookPath("guestunmount")
	if err != nil || guestUMountExec == "" {
		return fmt.Errorf("guestunmount not available")
	}

	buildGuestMountArgs := func() []string {
		base := []string{"-a", imagePath}
		if readonly {
			base = append(base, "--ro")
		}
		return base
	}

	tryGuestMount := func(args []string) (bool, string) {
		cmd := exec.Command(guestMountExec, args...)
		output, mountErr := cmd.CombinedOutput()
		if mountErr != nil {
			return false, strings.TrimSpace(string(output))
		}
		return true, ""
	}

	// Try OS inspection first for full OS images.
	if ok, _ := tryGuestMount(append(append(buildGuestMountArgs(), "-i"), mountPoint)); !ok {
		// If inspection fails (common for plain filesystem images), try explicit device mappings.
		if ok, _ := tryGuestMount(append(append(buildGuestMountArgs(), "-m", "/dev/sda1"), mountPoint)); !ok {
			ok, out := tryGuestMount(append(append(buildGuestMountArgs(), "-m", "/dev/sda"), mountPoint))
			if !ok {
				return fmt.Errorf("guestmount failed: %s", out)
			}
		}
	}

	d.mounted = true
	d.mountPoint = mountPoint
	d.unmountFunc = func() error {
		return exec.Command(guestUMountExec, mountPoint).Run()
	}
	return nil
}

func (d *BaseDrive) mountWithLoopDevice(imagePath, mountPoint, fsType, mountOpts string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("loop mount requires root privileges")
	}

	losetupCmd := exec.Command("losetup", "--find", "--show", imagePath)
	output, err := losetupCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to create loop device: %w", err)
	}
	loopDevice := strings.TrimSpace(string(output))

	mountCmd := exec.Command("mount", "-t", fsType, "-o", mountOpts, loopDevice, mountPoint)
	if output, err := mountCmd.CombinedOutput(); err != nil {
		_ = exec.Command("losetup", "-d", loopDevice).Run()
		return fmt.Errorf("failed to mount loop device: %w, output: %s", err, string(output))
	}

	d.mounted = true
	d.mountPoint = mountPoint
	d.unmountFunc = func() error {
		umountCmd := exec.Command("umount", mountPoint)
		if output, err := umountCmd.CombinedOutput(); err != nil {
			_ = exec.Command("losetup", "-d", loopDevice).Run()
			return fmt.Errorf("failed to unmount: %w, output: %s", err, string(output))
		}

		losetupCmd := exec.Command("losetup", "-d", loopDevice)
		if output, err := losetupCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to remove loop device: %w, output: %s", err, string(output))
		}

		return nil
	}

	return nil
}

func (d *BaseDrive) Unmount() error {
	d.lock.Lock()
	if !d.mounted {
		d.lock.Unlock()
		return ErrDriveNotMounted
	}
	if d.unMounting {
		d.lock.Unlock()
		return fmt.Errorf("drive is currently unmounting")
	}
	if d.unmountFunc == nil {
		d.lock.Unlock()
		return fmt.Errorf("drive has no unmount function")
	}
	d.unMounting = true
	d.lock.Unlock()

	d.ioWG.Wait()

	d.lock.Lock()
	unmountFunc := d.unmountFunc
	mountPoint := d.mountPoint
	ephemeral := d.params != nil && d.params.Ephemeral()
	d.lock.Unlock()

	if err := unmountFunc(); err != nil {
		d.lock.Lock()
		d.unMounting = false
		d.lock.Unlock()
		return fmt.Errorf("failed to unmount drive: %w", err)
	}

	if ephemeral && mountPoint != "" {
		if err := os.RemoveAll(mountPoint); err != nil {
			return fmt.Errorf("failed to remove ephemeral drive: %w", err)
		}
	}

	d.lock.Lock()
	d.mounted = false
	d.mountPoint = ""
	d.unmountFunc = nil
	d.unMounting = false
	d.lock.Unlock()
	return nil
}

func (d *BaseDrive) WriteToDrive(filePath string, r io.Reader, perm os.FileMode) error {
	if filePath == "" {
		return fmt.Errorf("%w: filepath cannot be empty", ErrInvalidParameter)
	}
	if d == nil || !d.Validate() {
		return ErrDriveNotValid
	}

	d.lock.Lock()
	if !d.mounted {
		d.lock.Unlock()
		return fmt.Errorf("drive not mounted")
	} else if d.unMounting {
		d.lock.Unlock()
		return fmt.Errorf("drive is currently unmounting")
	}

	readonly := d.params.ReadOnly()
	fsType := d.params.FSType()
	driveID := d.params.ID()
	mountPoint := d.mountPoint
	if readonly {
		d.lock.Unlock()
		return fmt.Errorf("cannot write to read-only drive: %s", driveID)
	}
	if fsType == "squashfs" {
		d.lock.Unlock()
		return fmt.Errorf("cannot write to squashfs drive (read-only filesystem): %s", driveID)
	}

	d.ioWG.Add(1)
	defer d.ioWG.Done()
	d.lock.Unlock()

	targetPath, err := pathCheck(mountPoint, filePath, true)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := noSymLinks(mountPoint, filepath.Dir(targetPath), false); err != nil {
		return err
	}

	if info, err := os.Lstat(targetPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: refusing to write to symlink path: %s", ErrInvalidParameter, filePath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect target file: %w", err)
	}

	f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}

	_, err = io.Copy(f, r)
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close file: %w", closeErr)
	}

	return nil
}

func (d *BaseDrive) LoadFromDrive(filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, fmt.Errorf("%w: filepath cannot be empty", ErrInvalidParameter)
	}
	if d == nil || !d.Validate() {
		return nil, ErrDriveNotValid
	}

	d.lock.Lock()
	if !d.mounted {
		d.lock.Unlock()
		return nil, fmt.Errorf("drive not mounted")
	} else if d.unMounting {
		d.lock.Unlock()
		return nil, fmt.Errorf("drive is currently unmounting")
	}

	d.ioWG.Add(1)
	defer d.ioWG.Done()

	mountPoint := d.mountPoint
	d.lock.Unlock()

	targetPath, err := pathCheck(mountPoint, filePath, false)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", filePath)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return data, nil
}

func (d *BaseDrive) Delete() error {
	if err := d.Unmount(); err != nil {
		if !errors.Is(err, ErrDriveNotMounted) {
			return err
		}
	}

	d.lock.Lock()
	ephemeral := d.params != nil && d.params.Ephemeral()
	imagePath := d.imagePath
	d.lock.Unlock()

	if ephemeral && imagePath != "" {
		if err := os.Remove(imagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove ephemeral drive image: %w", err)
		}
	}

	d.lock.Lock()
	d.mounted = false
	d.unMounting = false
	d.unmountFunc = nil
	d.mountPoint = ""
	d.params = nil
	d.imagePath = ""
	d.lock.Unlock()

	return nil
}
