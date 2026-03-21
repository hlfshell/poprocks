package drive

import "errors"

var (
	// ErrInvalidParameter indicates invalid function parameters.
	ErrInvalidParameter = errors.New("invalid parameter")
	// ErrDriveNotValid indicates the drive is not valid for the operation.
	ErrDriveNotValid = errors.New("drive not valid")
	// ErrDriveAlreadyMounted indicates the drive is already mounted.
	ErrDriveAlreadyMounted = errors.New("drive already mounted")
	// ErrDriveNotMounted indicates the drive is not mounted.
	ErrDriveNotMounted = errors.New("drive not mounted")
	// ErrNoMountMethodAvailable indicates no usable mount backend was detected.
	ErrNoMountMethodAvailable = errors.New("no usable mount method available")
	// ErrEncryptionUnavailable indicates encryption tooling is unavailable.
	ErrEncryptionUnavailable = errors.New("encryption tooling unavailable")
)
