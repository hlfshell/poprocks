package vm

import (
	"fmt"

	"github.com/hlfshell/poprocks/drive"
)

// EncryptedDriveOptions configures encrypted drive attachment to a VM.
type EncryptedDriveOptions struct {
	ID          string
	ReadOnly    bool
	IsRoot      bool
	PartUUID    string
	CacheType   string
	Ephemeral   bool
	SizeBytes   int64
	MountMethod drive.MountMethod

	// Key is preferred. If nil, Passphrase will be used to derive a key.
	Key drive.Key
	// Passphrase is used only when Key is nil.
	Passphrase string
}

// AttachDiskImage registers a drive for the given image file.
// If opts is nil, BaseDriveFromImage will auto-detect and create defaults.
func (v *VM) AttachDiskImage(imagePath string, opts *drive.DriveOptions) (*drive.BaseDrive, error) {
	driveObj, err := drive.BaseDriveFromImage(imagePath, opts)
	if err != nil {
		return nil, err
	}
	if err := v.addDriveLocked(driveObj); err != nil {
		return nil, err
	}
	return driveObj, nil
}

// AttachEncryptedDiskImage registers an encrypted drive for the given image file.
func (v *VM) AttachEncryptedDiskImage(imagePath string, opts EncryptedDriveOptions) (*drive.EncryptedDrive, error) {
	if imagePath == "" || opts.ID == "" {
		return nil, ErrInvalidParameter
	}

	key := opts.Key
	if key == nil {
		if opts.Passphrase == "" {
			return nil, fmt.Errorf("%w: passphrase cannot be empty", ErrInvalidParameter)
		}
		derived, err := drive.NewSHA256PassphraseKey([]byte(opts.Passphrase))
		if err != nil {
			return nil, err
		}
		key = derived
	}

	mountMethod := opts.MountMethod
	if mountMethod == "" {
		mountMethod = drive.MountMethodLoop
	}
	params := drive.NewDriveOptionsPtr(
		opts.ID,
		opts.ReadOnly,
		opts.IsRoot,
		opts.PartUUID,
		opts.CacheType,
		opts.Ephemeral,
		opts.SizeBytes,
		"ext4",
		mountMethod,
	)
	driveObj, err := drive.EncryptedDriveFromImage(imagePath, key, params)
	if err != nil {
		key.Destroy()
		return nil, err
	}
	if err := v.addDriveLocked(driveObj); err != nil {
		key.Destroy()
		return nil, err
	}
	return driveObj, nil
}

// AddDrive registers an unencrypted drive with the microVM configuration.
// This method cannot be called after Start() has been called.
func (v *VM) AddDrive(driveObj *drive.BaseDrive) error {
	return v.addDriveLocked(driveObj)
}

// AddEncryptedDrive registers an encrypted drive with the microVM configuration.
// This method cannot be called after Start() has been called.
func (v *VM) AddEncryptedDrive(driveObj *drive.EncryptedDrive) error {
	return v.addDriveLocked(driveObj)
}

func (v *VM) addDriveLocked(driveObj drive.Drive) error {
	if driveObj == nil || !driveObj.Validate() {
		return ErrInvalidParameter
	}

	params := driveObj.Parameters()
	if params == nil || params.ID() == "" {
		return ErrInvalidParameter
	}

	v.lock.Lock()
	defer v.lock.Unlock()

	if started, err := v.IsStarted(); err != nil {
		return err
	} else if started {
		return ErrVMAlreadyStarted
	}

	if v.hardware.Drives == nil {
		v.hardware.Drives = make(map[string]drive.Drive)
	}
	v.hardware.Drives[params.ID()] = driveObj
	return nil
}

// RemoveDrive removes a drive by ID.
// This method cannot be called after Start() has been called.
func (v *VM) RemoveDrive(driveID string) error {
	v.lock.Lock()
	defer v.lock.Unlock()

	if driveObj, ok := v.hardware.Drives[driveID]; !ok || driveObj == nil {
		return fmt.Errorf("unknown drive: %s", driveID)
	}

	if started, err := v.IsStarted(); err != nil {
		return err
	} else if started {
		return ErrVMAlreadyStarted
	}

	delete(v.hardware.Drives, driveID)
	return nil
}

func (v *VM) HotAddDrive(_ drive.Drive) error {
	v.lock.Lock()
	defer v.lock.Unlock()

	if started, err := v.IsStarted(); err != nil {
		return err
	} else if started {
		return ErrVMAlreadyStarted
	}

	return nil
}

func (v *VM) HotRemoveDrive(driveID string) error {
	v.lock.Lock()
	defer v.lock.Unlock()

	if driveObj, ok := v.hardware.Drives[driveID]; !ok || driveObj == nil {
		return fmt.Errorf("unknown drive: %s", driveID)
	}

	return nil
}
