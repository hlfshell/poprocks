package drive

import "encoding/json"

type MountMethod string

const (
	MountMethodGuestMount MountMethod = "guestmount"
	MountMethodLoop       MountMethod = "loop"
)

// DriveOptions are the common parameters we'd expect any drive to have.
// They are not editable once created.
type DriveOptions struct {
	id          string
	readonly    bool
	isRoot      bool
	partUUID    string
	cacheType   string
	ephemeral   bool
	sizeBytes   int64
	fsType      string
	mountMethod MountMethod
}

func NewDriveOptions(
	id string,
	readonly bool,
	isRoot bool,
	partUUID string,
	cacheType string,
	ephemeral bool,
	sizeBytes int64,
	fsType string,
	mountMethod MountMethod,
) DriveOptions {
	if fsType == "" {
		fsType = "ext4"
	}
	return DriveOptions{
		id:          id,
		readonly:    readonly,
		isRoot:      isRoot,
		partUUID:    partUUID,
		cacheType:   cacheType,
		ephemeral:   ephemeral,
		sizeBytes:   sizeBytes,
		fsType:      fsType,
		mountMethod: mountMethod,
	}
}

func NewDriveOptionsPtr(
	id string,
	readonly bool,
	isRoot bool,
	partUUID string,
	cacheType string,
	ephemeral bool,
	sizeBytes int64,
	fsType string,
	mountMethod MountMethod,
) *DriveOptions {
	opts := NewDriveOptions(id, readonly, isRoot, partUUID, cacheType, ephemeral, sizeBytes, fsType, mountMethod)
	return &opts
}

type driveOptionsSerializable struct {
	ID          string `json:"id"`
	ReadOnly    bool   `json:"readonly"`
	IsRoot      bool   `json:"isRoot"`
	PartUUID    string `json:"partUUID"`
	CacheType   string `json:"cacheType"`
	Ephemeral   bool   `json:"ephemeral"`
	SizeBytes   int64  `json:"sizeBytes"`
	FSType      string `json:"fsType"`
	MountMethod string `json:"mountMethod"`
}

func (p *DriveOptions) Validate() bool {
	if p.id == "" {
		return false
	}
	if p.sizeBytes <= 0 {
		return false
	}
	switch p.mountMethod {
	case "", MountMethodGuestMount, MountMethodLoop:
	default:
		return false
	}
	return true
}

func (p *DriveOptions) ID() string {
	return p.id
}

func (p *DriveOptions) ReadOnly() bool {
	return p.readonly
}

func (p *DriveOptions) IsRoot() bool {
	return p.isRoot
}

func (p *DriveOptions) PartUUID() string {
	return p.partUUID
}

func (p *DriveOptions) CacheType() string {
	return p.cacheType
}

func (p *DriveOptions) Ephemeral() bool {
	return p.ephemeral
}

func (p *DriveOptions) SizeBytes() int64 {
	return p.sizeBytes
}

func (p *DriveOptions) FSType() string {
	if p.fsType == "" {
		if p.readonly {
			return "squashfs"
		}
		return "ext4"
	}
	return p.fsType
}

func (p *DriveOptions) MountMethod() MountMethod {
	return p.mountMethod
}

func (p *DriveOptions) Clone() *DriveOptions {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

func (p *DriveOptions) MarshalJSON() ([]byte, error) {
	data := driveOptionsSerializable{
		ID:          p.id,
		ReadOnly:    p.readonly,
		IsRoot:      p.isRoot,
		PartUUID:    p.partUUID,
		CacheType:   p.cacheType,
		Ephemeral:   p.ephemeral,
		SizeBytes:   p.sizeBytes,
		FSType:      p.FSType(),
		MountMethod: string(p.mountMethod),
	}
	return json.Marshal(data)
}

func (p *DriveOptions) UnmarshalJSON(data []byte) error {
	var tmp driveOptionsSerializable
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	p.id = tmp.ID
	p.readonly = tmp.ReadOnly
	p.isRoot = tmp.IsRoot
	p.partUUID = tmp.PartUUID
	p.cacheType = tmp.CacheType
	p.ephemeral = tmp.Ephemeral
	p.sizeBytes = tmp.SizeBytes
	if tmp.FSType == "" {
		p.fsType = "ext4"
	} else {
		p.fsType = tmp.FSType
	}
	switch MountMethod(tmp.MountMethod) {
	case "":
		p.mountMethod = ""
	case MountMethodGuestMount:
		p.mountMethod = MountMethodGuestMount
	case MountMethodLoop:
		p.mountMethod = MountMethodLoop
	default:
		p.mountMethod = ""
	}
	return nil
}
