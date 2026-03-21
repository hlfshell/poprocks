package drive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func BaseDriveFromImage(imagePath string, options *DriveOptions) (*BaseDrive, error) {
	if imagePath == "" {
		return nil, fmt.Errorf("%w: image path cannot be empty", ErrInvalidParameter)
	}

	absImagePath, err := filepath.Abs(imagePath)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to resolve image path: %v", ErrInvalidParameter, err)
	}

	info, err := os.Stat(absImagePath)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to stat image path %q: %v", ErrInvalidParameter, absImagePath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: image path must be a file: %s", ErrInvalidParameter, absImagePath)
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("%w: image file is empty: %s", ErrInvalidParameter, absImagePath)
	}

	detectedFSType, detectedReadOnly, err := detectImageFilesystem(absImagePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParameter, err)
	}

	params := options.Clone()
	if params == nil {
		mountMethod, detectMountErr := DetectBestMountMethod()
		if detectMountErr != nil {
			return nil, detectMountErr
		}
		params = NewDriveOptionsPtr(
			deriveDriveIDFromPath(absImagePath),
			detectedReadOnly,
			false, // isRoot
			"",    // partUUID
			"",    // cacheType
			false, // ephemeral
			info.Size(),
			detectedFSType,
			mountMethod,
		)
	} else if params.mountMethod == "" {
		mountMethod, detectMountErr := DetectBestMountMethod()
		if detectMountErr != nil {
			return nil, detectMountErr
		}
		params.mountMethod = mountMethod
	}

	drive := &BaseDrive{
		params:    params,
		imagePath: absImagePath,
	}
	if !drive.Validate() {
		return nil, ErrDriveNotValid
	}

	if params.FSType() != detectedFSType {
		return nil, fmt.Errorf("%w: provided filesystem type %q does not match detected image type %q", ErrInvalidParameter, params.FSType(), detectedFSType)
	}
	if detectedReadOnly && !params.ReadOnly() {
		return nil, fmt.Errorf("%w: detected read-only image type %q requires readonly=true", ErrInvalidParameter, detectedFSType)
	}

	return drive, nil
}

func detectImageFilesystem(imagePath string) (fsType string, readOnly bool, err error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return "", false, fmt.Errorf("failed to open image file: %w", err)
	}
	defer file.Close()

	header := make([]byte, 4096)
	n, readErr := io.ReadFull(file, header)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", false, fmt.Errorf("failed to read image header: %w", readErr)
	}
	header = header[:n]

	if len(header) >= 4 && (bytes.Equal(header[:4], []byte("hsqs")) || bytes.Equal(header[:4], []byte("sqsh"))) {
		return "squashfs", true, nil
	}

	if len(header) >= 1082 && header[1080] == 0x53 && header[1081] == 0xEF {
		return "ext4", false, nil
	}

	if len(header) >= 4 && bytes.Equal(header[:4], []byte{'Q', 'F', 'I', 0xfb}) {
		return "", false, fmt.Errorf("unsupported image format qcow2; expected a filesystem image (raw ext4/squashfs)")
	}

	if blkidPath, lookErr := exec.LookPath("blkid"); lookErr == nil && blkidPath != "" {
		out, cmdErr := exec.Command(blkidPath, "-o", "value", "-s", "TYPE", imagePath).CombinedOutput()
		if cmdErr == nil {
			blkidType := strings.TrimSpace(string(out))
			if blkidType != "" {
				return blkidType, blkidType == "squashfs", nil
			}
		}
	}

	return "", false, fmt.Errorf("could not detect a supported filesystem image type")
}

func deriveDriveIDFromPath(imagePath string) string {
	baseName := filepath.Base(imagePath)
	extension := filepath.Ext(baseName)
	id := strings.TrimSuffix(baseName, extension)
	id = strings.TrimSpace(id)
	id = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '-'
		}
	}, id)
	id = strings.Trim(id, "-_.")
	if id == "" {
		return "drive"
	}
	return id
}
