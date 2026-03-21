package vm

import (
	"sync"

	"github.com/hlfshell/poprocks/drive"

	"github.com/firecracker-microvm/firecracker-go-sdk"
)

type VM struct {
	config Config
	hardware Hardware

	firecracker.Machine

	lock sync.Mutex
}

// NewVM constructs a VM from required hardware and runtime config.
func NewVM(hardware Hardware, config Config) (*VM, error) {
	if err := hardware.Validate(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if hardware.Drives == nil {
		hardware.Drives = make(map[string]drive.Drive)
	}
	return &VM{
		hardware: hardware,
		config:   config,
	}, nil
}

func (v *VM) Stop() error {
	v.lock.Lock()
	defer v.lock.Unlock()

	isStarted, err := v.IsStarted()
	if err != nil {
		return err
	}
	if !isStarted {
		return ErrVMNotStarted
	}

	// Stop the VMM process
	if err := v.Machine.StopVMM(); err != nil {
		// Ignore errors if VMM is already stopped
		// (StopVMM may return an error if the process is already gone)
	}
	return nil
}

// Cleanup stops the Firecracker VMM.
// To do - determine artifact behavior.
func (v *VM) Cleanup() error {
	v.lock.Lock()
	defer v.lock.Unlock()

	return v.Machine.StopVMM()
}
