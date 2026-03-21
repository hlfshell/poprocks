package vm

import (
	"fmt"

	"github.com/hlfshell/poprocks/drive"

	"github.com/firecracker-microvm/firecracker-go-sdk"
)

// Hardware defines VM hardware and attached devices.
type Hardware struct {
	// Network controls network access for the VM.
	// If nil, no network access is allowed.
	Network *NetworkConfig

	// Drives define Firecracker block devices backed by disk images.
	Drives map[string]drive.Drive

	// CPU and memory configuration for the VM.
	RAMMib int
	CPUs   int
	// Symmetric Multitasking (SMT) is a feature that allows the VM to use
	// multiple threads of execution per CPU core. It is typically disabled
	// by default due to security concerns (side-channel risks) and host CPU
	// topology assumptions, not due to instability or performance issues.
	SymmetricMultitasking bool

	Sockets SocketConfig
}

// TODO
type NetworkConfig struct{}

// NewNetworkConfig returns a placeholder network config.
func NewNetworkConfig(strict bool) *NetworkConfig {
	_ = strict
	return &NetworkConfig{}
}

// NewSockConfig creates a new SockConfig with default values.
// GuestCID defaults to 3 (host uses 2), GuestVSockPort defaults to 1.
func NewSockConfig() *SocketConfig {
	return &SocketConfig{
		GuestCID:       3,
		GuestVSockPort: 1,
	}
}

type SocketConfig struct {
	// GuestCID (Context ID) used by the vsock device attached to this VM.
	// CID is a unique identifier for a VM in the vsock address space.
	// The host uses CID=2 (Firecracker default), and the guest typically uses CID=3.
	// Must not be 2 and should be >2.
	GuestCID int
	// GuestVSockPort is the guest port where /init listens for framed requests.
	// Framed requests use a protocol where each message is prefixed with a
	// 4-byte big-endian uint32 length field, followed by the payload bytes.
	// Must be in range 1-65535.
	GuestVSockPort int

	// APISock is the Unix domain socket (UDS) used by the Firecracker SDK to send
	// configuration commands to the VMM (Virtual Machine Monitor) process.
	// Typically: <WorkDir>/<runID>/fc.api.sock
	// If empty, will be computed from WorkDir and runID.
	APISock string
	// VsockUDS is the Unix domain socket (UDS) used by the Firecracker SDK to bridge host
	// connections to the guest's AF_VSOCK socket.
	// Typically: <WorkDir>/<runID>/fc.vsock.sock
	// If empty, will be computed from WorkDir and runID.
	VsockUDS string
	// LogPath is the path to the Firecracker log file.
	// Typically: <WorkDir>/<runID>/fc.log
	// If empty, will be computed from WorkDir and runID.
	LogPath string
	// MetricsPath is the path to the Firecracker metrics file.
	// Typically: <WorkDir>/<runID>/fc.metrics
	// If empty, will be computed from WorkDir and runID.
	MetricsPath string

	// VsockDevices are the vsock devices to attach to the VM.
	VsockDevices []*firecracker.VsockDevice
}

// Validate checks that GuestCID and GuestVSockPort are valid.
func (s *SocketConfig) Validate() error {
	if s.GuestCID == 2 {
		return fmt.Errorf("%w: GuestCID must not be 2 (reserved for host)", ErrConfigValidation)
	}
	if s.GuestCID <= 0 {
		return fmt.Errorf("%w: GuestCID must be > 0, got %d", ErrConfigValidation, s.GuestCID)
	}
	if s.GuestVSockPort < 1 || s.GuestVSockPort > 65535 {
		return fmt.Errorf("%w: GuestVSockPort must be in range 1-65535, got %d", ErrConfigValidation, s.GuestVSockPort)
	}
	return nil
}

// NewHardware creates hardware with initialized collections and default socket values.
func NewHardware() *Hardware {
	return &Hardware{
		Drives:  make(map[string]drive.Drive),
		Sockets: *NewSockConfig(),
	}
}

// Validate checks that the hardware configuration is valid.
func (h *Hardware) Validate() error {
	if h.CPUs <= 0 {
		return fmt.Errorf("%w: CPUs must be > 0, got %d", ErrConfigValidation, h.CPUs)
	}
	if h.RAMMib <= 0 {
		return fmt.Errorf("%w: RAMMib must be > 0, got %d", ErrConfigValidation, h.RAMMib)
	}

	hasRoot := false
	for driveID, driveObj := range h.Drives {
		if driveObj == nil {
			return fmt.Errorf("%w: unknown drive: %s", ErrConfigValidation, driveID)
		}
		params := driveObj.Parameters()
		if params == nil {
			return fmt.Errorf("%w: drive has nil parameters: %s", ErrConfigValidation, driveID)
		}
		if driveID != params.ID() {
			return fmt.Errorf("%w: drive ID mismatch: %s != %s", ErrConfigValidation, driveID, params.ID())
		}
		if !driveObj.Validate() {
			return fmt.Errorf("%w: invalid drive: %s", ErrConfigValidation, params.ID())
		}
		hasRoot = hasRoot || params.IsRoot()
	}
	// Validate that at least one root drive exists if drives are present
	if len(h.Drives) > 0 && !hasRoot {
		return fmt.Errorf("%w: at least one drive must be marked as root", ErrConfigValidation)
	}
	if err := h.Sockets.Validate(); err != nil {
		return err
	}

	return nil
}
