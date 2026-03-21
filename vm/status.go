package vm

import (
	"context"
	"fmt"
	"os"
	"time"
)

type Status string

const (
	// UNKNOWN - the state is unable to be determined due to some error
	UNKNOWN Status = "unknown"
	// CREATED - the MicroVM object exists but the Firecracker VM has not
	// been started yet. Since Firecracker VMs are ephemeral, this represents a
	// configured but not yet running VM.
	CREATED  Status = "created"
	STARTING Status = "starting"
	RUNNING  Status = "running"
	STOPPING Status = "stopping"
	// STOPPED - the machine is stopped
	STOPPED Status = "stopped"
	ERROR   Status = "error"
)

// Private helper function to check status; done to allow
// calling this without requiring a lock due to race conditions.
func (v *VM) statusCheck() (Status, error) {
	// Check if API socket exists - if not, VM hasn't been started
	// but we exist, so we're defaulting to created.
	if v.hardware.Sockets.APISock == "" {
		return CREATED, nil
	}

	// Check if API socket file exists on filesystem
	if _, err := os.Stat(v.hardware.Sockets.APISock); os.IsNotExist(err) {
		// Socket doesn't exist, VM hasn't been started or was cleaned up
		return CREATED, nil
	}

	// Try to query the machine state via Firecracker API
	// Use a short timeout to avoid hanging
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Attempt to get instance info from Firecracker API
	// If this succeeds, the VM is running and responding
	_, err := v.Machine.DescribeInstanceInfo(ctx)
	if err != nil {
		// If we can't connect to the API, the VM might be stopped or in error state
		// Check if it's a connection error
		if ctx.Err() == context.DeadlineExceeded {
			// Timeout - VM might be starting or stopped
			return UNKNOWN, nil
		}
		// Connection refused or other errors indicate VM is not running
		return STOPPED, nil
	}

	// Successfully got instance info - VM is running
	return RUNNING, nil
}

// IsStarted checks if the VM is started and thus can be booted; statuses
// are: STARTING, RUNNING, STOPPING
func (v *VM) IsStarted() (bool, error) {
	v.lock.Lock()
	defer v.lock.Unlock()
	status, err := v.statusCheck()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrCannotDetermineStatus, err)
	}
	return status == RUNNING || status == STARTING || status == STOPPING, nil
}

func (v *VM) Status() (Status, error) {
	v.lock.Lock()
	defer v.lock.Unlock()
	return v.statusCheck()
}

// Start will attempt to start the VM.
// Errors:
// - ErrCannotDetermineStatus: if the status of the VM cannot be determined
// - ErrVMAlreadyStarted: if the VM has already been started
// - ErrFailedToStartVM: if the VM failed to start
func (v *VM) Start() error {
	v.lock.Lock()
	defer v.lock.Unlock()

	// Mark as started - no more drive mutations allowed
	isStarted, err := v.IsStarted()
	if err != nil {
		return err
	}
	if isStarted {
		return ErrVMAlreadyStarted
	}

	// Start the machine
	// TODO - implement max timeout / heartbeat check logic
	ctx, cancel := context.WithTimeout(context.Background(), v.config.StartupTimeout)
	defer cancel()
	if err := v.Machine.Start(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrFailedToStartVM, err)
	}
	return nil
}
