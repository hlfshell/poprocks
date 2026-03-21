package drive

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DetectBestMountMethod determines the best mount backend supported by the host.
// Preference is guestmount first (works without root), then loop mounts.
func DetectBestMountMethod() (MountMethod, error) {
	guestMountPath, guestMountErr := exec.LookPath("guestmount")
	guestUMountPath, guestUMountErr := exec.LookPath("guestunmount")
	if guestMountErr == nil && guestMountPath != "" && guestUMountErr == nil && guestUMountPath != "" {
		return MountMethodGuestMount, nil
	}

	losetupPath, losetupErr := exec.LookPath("losetup")
	mountPath, mountErr := exec.LookPath("mount")
	umountPath, umountErr := exec.LookPath("umount")
	if os.Geteuid() == 0 && losetupErr == nil && losetupPath != "" && mountErr == nil && mountPath != "" && umountErr == nil && umountPath != "" {
		return MountMethodLoop, nil
	}

	guestReason := reasonFromTooling(guestMountErr, guestUMountErr)
	loopReason := reasonFromLoopTooling(losetupErr, mountErr, umountErr)
	return "", fmt.Errorf("%w: guestmount=%s; loop=%s", ErrNoMountMethodAvailable, guestReason, loopReason)
}

func reasonFromTooling(guestMountErr, guestUMountErr error) string {
	reasons := make([]string, 0, 2)
	if guestMountErr != nil {
		reasons = append(reasons, "guestmount missing")
	}
	if guestUMountErr != nil {
		reasons = append(reasons, "guestunmount missing")
	}
	if len(reasons) == 0 {
		return "ok"
	}
	return strings.Join(reasons, ", ")
}

func reasonFromLoopTooling(losetupErr, mountErr, umountErr error) string {
	reasons := make([]string, 0, 4)
	if os.Geteuid() != 0 {
		reasons = append(reasons, "requires root")
	}
	if losetupErr != nil {
		reasons = append(reasons, "losetup missing")
	}
	if mountErr != nil {
		reasons = append(reasons, "mount missing")
	}
	if umountErr != nil {
		reasons = append(reasons, "umount missing")
	}
	if len(reasons) == 0 {
		return "ok"
	}
	return strings.Join(reasons, ", ")
}
