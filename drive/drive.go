package drive

import (
	"io"
	"os"
)

type Drive interface {
	Validate() bool

	Parameters() *DriveOptions
	ImagePath() string

	WriteToDrive(path string, reader io.Reader, permissions os.FileMode) error
	LoadFromDrive(filePath string) ([]byte, error)

	Mounted() bool
	Mount(mountPath string) error
	Unmount() error

	Delete() error
}
