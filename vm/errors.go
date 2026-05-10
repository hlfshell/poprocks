package vm

import "errors"

// Errors that can be checked by callers using errors.Is.
var (
	// ErrInvalidParameter indicates invalid function parameters (e.g., empty paths).
	ErrInvalidParameter = errors.New("invalid parameter")
	// ErrCannotDetermineStatus indicates the status of the VM cannot be ascertained.
	ErrCannotDetermineStatus = errors.New("cannot determine status")
	// ErrVMAlreadyStarted indicates the VM has already been started.
	ErrVMAlreadyStarted = errors.New("VM already started")
	// ErrVMNotStarted indicates the VM has not been started.
	ErrVMNotStarted = errors.New("VM not started")
	// ErrFailedToStartVM indicates the VM failed to start.
	ErrFailedToStartVM = errors.New("failed to start VM")

	// ErrEnvInvalidFile indicates the environment file is malformed.
	ErrEnvInvalidFile = errors.New("invalid environment file")
)
