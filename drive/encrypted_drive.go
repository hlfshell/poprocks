package drive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// EncryptedDrive is a LUKS/dm-crypt-backed drive implementation.
type EncryptedDrive struct {
	params    *DriveOptions
	imagePath string
	key       Key

	mounted     bool
	mountPoint  string
	unmountFunc func() error

	ioWG       sync.WaitGroup
	unMounting bool

	lock sync.Mutex
}

func EncryptedDriveFromImage(imagePath string, key Key, options *DriveOptions) (*EncryptedDrive, error) {
	if imagePath == "" {
		return nil, fmt.Errorf("%w: image path cannot be empty", ErrInvalidParameter)
	}
	if key == nil {
		return nil, fmt.Errorf("%w: key cannot be nil", ErrInvalidParameter)
	}

	absImagePath, err := filepath.Abs(imagePath)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to resolve image path: %v", ErrInvalidParameter, err)
	}

	params := options.Clone()
	if params == nil {
		info, statErr := os.Stat(absImagePath)
		if statErr != nil {
			return nil, fmt.Errorf("%w: options are required to create a new encrypted drive image", ErrInvalidParameter)
		}
		params = NewDriveOptionsPtr(
			deriveDriveIDFromPath(absImagePath),
			false,
			false,
			"",
			"",
			false,
			info.Size(),
			"ext4",
			MountMethodLoop,
		)
	}
	if params.FSType() != "ext4" {
		return nil, fmt.Errorf("%w: encrypted drive currently supports ext4 only", ErrInvalidParameter)
	}

	drive := &EncryptedDrive{
		params:    params,
		imagePath: absImagePath,
		key:       key,
	}
	if !drive.Validate() {
		return nil, ErrDriveNotValid
	}
	return drive, nil
}

func (d *EncryptedDrive) Validate() bool {
	if d == nil {
		return false
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if d.params == nil || !d.params.Validate() {
		return false
	}
	if d.imagePath == "" || d.key == nil {
		return false
	}
	if d.params.FSType() != "ext4" {
		return false
	}
	return true
}

func (d *EncryptedDrive) Parameters() *DriveOptions {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.params.Clone()
}

func (d *EncryptedDrive) ImagePath() string {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.imagePath
}

func (d *EncryptedDrive) Mounted() bool {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.mounted
}

func (d *EncryptedDrive) Mount(mountPoint string) error {
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
	if os.Geteuid() != 0 {
		return fmt.Errorf("encrypted drive mount requires root privileges")
	}
	if err := ensureEncryptionTooling(); err != nil {
		return err
	}

	if mountPoint == "" {
		var err error
		mountPoint, err = os.MkdirTemp("", "encrypted-drive-mount-")
		if err != nil {
			return fmt.Errorf("failed to create mount point: %w", err)
		}
	}

	if err := d.ensureLUKSImage(); err != nil {
		return err
	}

	mapName := d.mapName()
	mapperPath := d.mapperPath()
	if err := d.runCryptsetupWithKey("luksOpen", d.imagePath, mapName, "--key-file", "-"); err != nil {
		return fmt.Errorf("failed to open encrypted drive: %w", err)
	}

	mountOpts := "nosuid,nodev,noexec"
	if d.params.ReadOnly() {
		mountOpts += ",ro"
	}
	if err := runCmd("mount", "-t", d.params.FSType(), "-o", mountOpts, mapperPath, mountPoint); err != nil {
		_ = runCmd("cryptsetup", "luksClose", mapName)
		return fmt.Errorf("failed to mount encrypted mapper: %w", err)
	}

	d.mounted = true
	d.mountPoint = mountPoint
	d.unmountFunc = func() error {
		if err := runCmd("umount", mountPoint); err != nil {
			_ = runCmd("cryptsetup", "luksClose", mapName)
			return err
		}
		return runCmd("cryptsetup", "luksClose", mapName)
	}
	return nil
}

func (d *EncryptedDrive) Unmount() error {
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

func (d *EncryptedDrive) WriteToDrive(filePath string, r io.Reader, perm os.FileMode) error {
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
	}
	if d.unMounting {
		d.lock.Unlock()
		return fmt.Errorf("drive is currently unmounting")
	}
	if d.params.ReadOnly() {
		driveID := d.params.ID()
		d.lock.Unlock()
		return fmt.Errorf("cannot write to read-only drive: %s", driveID)
	}
	mountPoint := d.mountPoint
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

func (d *EncryptedDrive) LoadFromDrive(filePath string) ([]byte, error) {
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
	}
	if d.unMounting {
		d.lock.Unlock()
		return nil, fmt.Errorf("drive is currently unmounting")
	}
	mountPoint := d.mountPoint
	d.ioWG.Add(1)
	defer d.ioWG.Done()
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

func (d *EncryptedDrive) Delete() error {
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
	if d.key != nil {
		d.key.Destroy()
	}
	d.key = nil
	d.lock.Unlock()
	return nil
}

func (d *EncryptedDrive) ensureLUKSImage() error {
	if _, err := os.Stat(d.imagePath); err == nil {
		if err := runCmd("cryptsetup", "isLuks", d.imagePath); err != nil {
			return fmt.Errorf("%w: existing image is not a valid LUKS container: %v", ErrInvalidParameter, err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect encrypted image: %w", err)
	}

	if d.params.SizeBytes() <= 0 {
		return fmt.Errorf("%w: sizeBytes must be > 0 to create encrypted image", ErrInvalidParameter)
	}
	if err := os.MkdirAll(filepath.Dir(d.imagePath), 0o755); err != nil {
		return fmt.Errorf("failed to create encrypted image directory: %w", err)
	}
	if err := runCmd("truncate", "-s", fmt.Sprintf("%d", d.params.SizeBytes()), d.imagePath); err != nil {
		return fmt.Errorf("failed to create encrypted image: %w", err)
	}

	mapName := d.mapName()
	if err := d.runCryptsetupWithKey("--batch-mode", "--type", "luks2", "luksFormat", d.imagePath, "--key-file", "-"); err != nil {
		return fmt.Errorf("failed to format encrypted image: %w", err)
	}
	if err := d.runCryptsetupWithKey("luksOpen", d.imagePath, mapName, "--key-file", "-"); err != nil {
		return fmt.Errorf("failed to open newly formatted encrypted image: %w", err)
	}
	defer runCmd("cryptsetup", "luksClose", mapName)

	if err := runCmd("mkfs.ext4", "-F", d.mapperPath()); err != nil {
		return fmt.Errorf("failed to format encrypted mapper filesystem: %w", err)
	}
	return nil
}

func ensureEncryptionTooling() error {
	tools := []string{"cryptsetup", "truncate", "mkfs.ext4", "mount", "umount"}
	missing := make([]string, 0, len(tools))
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing tools: %s", ErrEncryptionUnavailable, strings.Join(missing, ", "))
	}
	return nil
}

func (d *EncryptedDrive) mapName() string {
	sum := sha256.Sum256([]byte(d.imagePath))
	return "drvcrypt-" + hex.EncodeToString(sum[:6])
}

func (d *EncryptedDrive) mapperPath() string {
	return "/dev/mapper/" + d.mapName()
}

func (d *EncryptedDrive) runCryptsetupWithKey(args ...string) error {
	return d.key.WithReader(func(r io.Reader) error {
		return runCmdWithStdin(r, "cryptsetup", args...)
	})
}

func runCmd(name string, args ...string) error {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("command %s %s failed: %w; output=%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runCmdWithStdin(stdin io.Reader, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command %s %s failed: %w; output=%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
