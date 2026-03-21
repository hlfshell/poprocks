package drive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func pathCheck(mountPoint, filePath string, allowMissingTail bool) (string, error) {
	safePath, err := safeDrivePath(mountPoint, filePath)
	if err != nil {
		return "", err
	}
	if err := noSymLinks(mountPoint, safePath, allowMissingTail); err != nil {
		return "", err
	}
	return safePath, nil
}

// safeDrivePath prevents traversal attacks by ensuring a file path resolves
// under the provided mount point.
func safeDrivePath(mountPoint, filePath string) (string, error) {
	rel := strings.TrimPrefix(filePath, "/")
	if rel == "" {
		rel = "."
	}

	targetPath := filepath.Clean(filepath.Join(mountPoint, rel))
	mountPointAbs, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute mount point: %w", err)
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute target path: %w", err)
	}

	relPath, err := filepath.Rel(mountPointAbs, targetAbs)
	if err != nil {
		return "", fmt.Errorf("failed to check path traversal: %w", err)
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path traversal detected: %s", ErrInvalidParameter, filePath)
	}

	return targetAbs, nil
}

func noSymLinks(mountPoint, targetPath string, allowMissingTail bool) error {
	mountAbs, err := filepath.Abs(mountPoint)
	if err != nil {
		return fmt.Errorf("failed to resolve mount point: %w", err)
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("failed to resolve target path: %w", err)
	}

	relPath, err := filepath.Rel(mountAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("failed to evaluate target path: %w", err)
	}
	if relPath == "." {
		return nil
	}

	segments := strings.Split(relPath, string(os.PathSeparator))
	currentPath := mountAbs
	for idx, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, segment)
		info, err := os.Lstat(currentPath)
		if err != nil {
			if allowMissingTail && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("failed to inspect path segment %q: %w", currentPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink segment is not allowed in drive path %q (segment %d)", ErrInvalidParameter, filePathForError(mountAbs, currentPath), idx)
		}
	}

	return nil
}

func filePathForError(mountPoint, target string) string {
	rel, err := filepath.Rel(mountPoint, target)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}
