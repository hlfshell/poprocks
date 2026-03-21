package vm

import "errors"

// Errors that can be checked by callers using errors.Is.
var (
	// ErrSourceNotFound indicates the source path does not exist.
	ErrSourceNotFound = errors.New("source path not found")
	// ErrSourceNotDirectory indicates the source path exists but is not a directory.
	ErrSourceNotDirectory = errors.New("source is not a directory")
	// ErrDestinationExists indicates the destination path already exists and cannot be overwritten.
	ErrDestinationExists = errors.New("destination path already exists")
	// ErrInvalidParameter indicates invalid function parameters (e.g., empty paths).
	ErrInvalidParameter = errors.New("invalid parameter")
	// ErrCopyFailed indicates a failure during the copy operation.
	ErrCopyFailed = errors.New("copy operation failed")
	// ErrCannotDetermineStatus indicates the status of the VM cannot be ascertained.
	ErrCannotDetermineStatus = errors.New("cannot determine status")
	// ErrVMAlreadyStarted indicates the VM has already been started.
	ErrVMAlreadyStarted = errors.New("VM already started")
	// ErrVMNotStarted indicates the VM has not been started.
	ErrVMNotStarted = errors.New("VM not started")
	// ErrFailedToStartVM indicates the VM failed to start.
	ErrFailedToStartVM = errors.New("failed to start VM")
	// ErrDriveNotValid indicates the drive is not valid for the operation.
	ErrDriveNotValid = errors.New("drive not valid")
	// ErrDriveAlreadyMounted indicates the drive is already mounted.
	ErrDriveAlreadyMounted = errors.New("drive already mounted")
	// ErrDriveNotMounted indicates the drive is not mounted.
	ErrDriveNotMounted = errors.New("drive not mounted")
)
