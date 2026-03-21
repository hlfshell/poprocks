package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config defines runtime execution configuration for a VM.
type Config struct {
	Firecracker FirecrackerConfig

	// StartupTimeout waits for the first initial "ready" signal from the VM to
	// indicate we are booted. If it sends back a heartbeat of an "initializing"
	// state, then we grant it more time, up to the MaxStartupTimeout.
	StartupTimeout    time.Duration
	MaxStartupTimeout time.Duration
}

// Validate validates the entire configuration.
func (c *Config) Validate() error {
	if err := c.Firecracker.Validate(); err != nil {
		return err
	}
	return nil
}

var (
	// ErrConfigValidation indicates a configuration validation error.
	ErrConfigValidation = errors.New("configuration validation failed")
)

type FirecrackerConfig struct {
	// FirecrackerBin is the path to the Firecracker executable.
	FirecrackerBin string
	// KernelImage is the path to the kernel image file.
	KernelImage string
	// InitrdImage is the path to the initrd image file.
	InitrdImage string
	// WorkDir is the working directory for VM runtime files.
	// This will be expanded to an absolute path during Init().
	WorkDir string
}

// Init initializes the FirecrackerConfig with defaults from environment
// variables, then validates paths and creates the work directory.
// It expands WorkDir to an absolute path for stable path joins.
func (c *FirecrackerConfig) Init() error {
	if c.FirecrackerBin == "" {
		c.FirecrackerBin = os.Getenv("FIRECRACKER_BIN")
		if c.FirecrackerBin == "" {
			c.FirecrackerBin = "/usr/local/bin/firecracker"
		}
	}
	if c.KernelImage == "" {
		c.KernelImage = os.Getenv("KERNEL_IMAGE")
		if c.KernelImage == "" {
			c.KernelImage = "./artifacts/vmlinux"
		}
	}
	if c.InitrdImage == "" {
		c.InitrdImage = os.Getenv("INITRD_IMAGE")
		if c.InitrdImage == "" {
			c.InitrdImage = "./artifacts/initramfs.cpio.gz"
		}
	}
	if c.WorkDir == "" {
		c.WorkDir = os.Getenv("WORK_DIR")
		if c.WorkDir == "" {
			c.WorkDir = "./run"
		}
	}

	// Expand WorkDir to absolute path
	absWorkDir, err := filepath.Abs(c.WorkDir)
	if err != nil {
		return fmt.Errorf("%w: failed to expand WorkDir: %w", ErrConfigValidation, err)
	}
	c.WorkDir = absWorkDir

	// Validate paths exist
	if _, err := os.Stat(c.FirecrackerBin); err != nil {
		return fmt.Errorf("%w: FirecrackerBin not found: %s: %w", ErrConfigValidation, c.FirecrackerBin, err)
	}
	if _, err := os.Stat(c.KernelImage); err != nil {
		return fmt.Errorf("%w: KernelImage not found: %s: %w", ErrConfigValidation, c.KernelImage, err)
	}
	if _, err := os.Stat(c.InitrdImage); err != nil {
		return fmt.Errorf("%w: InitrdImage not found: %s: %w", ErrConfigValidation, c.InitrdImage, err)
	}

	// Create work directory
	if err := os.MkdirAll(c.WorkDir, 0o755); err != nil {
		return fmt.Errorf("%w: failed to create WorkDir: %w", ErrConfigValidation, err)
	}

	return nil
}

// Validate checks that all required fields are set and paths exist.
func (c *FirecrackerConfig) Validate() error {
	if c.FirecrackerBin == "" {
		return fmt.Errorf("%w: FirecrackerBin is required", ErrConfigValidation)
	}
	if c.KernelImage == "" {
		return fmt.Errorf("%w: KernelImage is required", ErrConfigValidation)
	}
	if c.InitrdImage == "" {
		return fmt.Errorf("%w: InitrdImage is required", ErrConfigValidation)
	}
	if c.WorkDir == "" {
		return fmt.Errorf("%w: WorkDir is required", ErrConfigValidation)
	}

	if _, err := os.Stat(c.FirecrackerBin); err != nil {
		return fmt.Errorf("%w: FirecrackerBin not found: %s: %w", ErrConfigValidation, c.FirecrackerBin, err)
	}
	if _, err := os.Stat(c.KernelImage); err != nil {
		return fmt.Errorf("%w: KernelImage not found: %s: %w", ErrConfigValidation, c.KernelImage, err)
	}
	if _, err := os.Stat(c.InitrdImage); err != nil {
		return fmt.Errorf("%w: InitrdImage not found: %s: %w", ErrConfigValidation, c.InitrdImage, err)
	}

	return nil
}
