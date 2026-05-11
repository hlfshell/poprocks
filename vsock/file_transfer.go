package vsock

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	TypeFileTransferOpen   uint32 = 0xFFFF0001
	TypeFileTransferBody   uint32 = 0xFFFF0002
	TypeFileTransferCommit uint32 = 0xFFFF0003
)

// FileTransferRequest opens a transfer session before streaming file bytes.
type FileTransferRequest struct {
	TransferID  string `msgpack:"transfer_id"`
	Name        string `msgpack:"name"`
	Destination string `msgpack:"destination,omitempty"`
	Size        int64  `msgpack:"size"`
	SHA256      string `msgpack:"sha256"`
}

// FileTransferResponse indicates whether the receiver accepted a transfer.
type FileTransferResponse struct {
	TransferID string `msgpack:"transfer_id"`
	Accepted   bool   `msgpack:"accepted"`
	Error      string `msgpack:"error,omitempty"`
}

// FileTransferCommit asks the receiver to finalize a streamed transfer.
type FileTransferCommit struct {
	TransferID string `msgpack:"transfer_id"`
}

// FileTransferResult reports the finalized transfer outcome.
type FileTransferResult struct {
	TransferID string `msgpack:"transfer_id"`
	OK         bool   `msgpack:"ok"`
	Size       int64  `msgpack:"size"`
	SHA256     string `msgpack:"sha256,omitempty"`
	Error      string `msgpack:"error,omitempty"`
}

// FileTransferPlan is chosen by the receiver and determines where a file lands.
type FileTransferPlan struct {
	DestinationPath string
}

type fileTransferBody struct {
	TransferID string
	Reader     io.Reader
	Length     uint32
}

func (p fileTransferBody) StreamSource() (io.Reader, uint32, error) {
	if p.Reader == nil {
		return nil, 0, fmt.Errorf("reader is required")
	}
	header, err := encodeTransferBodyHeader(p.TransferID)
	if err != nil {
		return nil, 0, err
	}
	total := uint64(len(header)) + uint64(p.Length)
	if total > uint64(^uint32(0)) {
		return nil, 0, fmt.Errorf("stream body too large")
	}
	return io.MultiReader(bytesReader(header), p.Reader), uint32(total), nil
}

type incomingFileTransfer struct {
	lock         sync.Mutex
	finalPath    string
	tempPath     string
	file         *os.File
	hash         hash.Hash
	expected     FileTransferRequest
	bodyWritten  bool
	bytesWritten int64
}

// FileTransfer provides a built-in request plus stream file transfer protocol.
type FileTransfer struct {
	open      *R[FileTransferRequest, FileTransferResponse]
	body      *M[fileTransferBody]
	commit    *R[FileTransferCommit, FileTransferResult]
	lock      sync.Mutex
	incoming  map[string]*incomingFileTransfer
	onReceive func(context.Context, FileTransferRequest) (FileTransferPlan, error)
}

// NewFileTransfer wires a file transfer protocol onto an existing messenger.
func NewFileTransfer(messenger *Messenger) (*FileTransfer, error) {
	openReqCodec, err := NewCodecOfType[FileTransferRequest](TypeFileTransferOpen, CodecMsgpack)
	if err != nil {
		return nil, err
	}
	openRespCodec, err := NewCodecOfType[FileTransferResponse](TypeFileTransferOpen+100, CodecMsgpack)
	if err != nil {
		return nil, err
	}
	bodyCodec, err := NewCodecOfType[fileTransferBody](TypeFileTransferBody, CodecStream)
	if err != nil {
		return nil, err
	}
	commitReqCodec, err := NewCodecOfType[FileTransferCommit](TypeFileTransferCommit, CodecMsgpack)
	if err != nil {
		return nil, err
	}
	commitRespCodec, err := NewCodecOfType[FileTransferResult](TypeFileTransferCommit+100, CodecMsgpack)
	if err != nil {
		return nil, err
	}

	open, err := NewR[FileTransferRequest, FileTransferResponse](messenger, openReqCodec, openRespCodec)
	if err != nil {
		return nil, err
	}
	body, err := NewM[fileTransferBody](messenger, bodyCodec)
	if err != nil {
		return nil, err
	}
	commit, err := NewR[FileTransferCommit, FileTransferResult](messenger, commitReqCodec, commitRespCodec)
	if err != nil {
		return nil, err
	}

	ft := &FileTransfer{
		open:     open,
		body:     body,
		commit:   commit,
		incoming: make(map[string]*incomingFileTransfer),
	}

	open.OnRequest(ft.handleOpen)
	body.OnReceiveStream(ft.handleBody)
	commit.OnRequest(ft.handleCommit)

	return ft, nil
}

// OnReceive installs the receiver-side destination policy for incoming files.
func (f *FileTransfer) OnReceive(handler func(context.Context, FileTransferRequest) (FileTransferPlan, error)) {
	if f == nil {
		return
	}
	f.lock.Lock()
	defer f.lock.Unlock()
	f.onReceive = handler
}

// SendFile negotiates, streams, and finalizes a single regular file transfer.
func (f *FileTransfer) SendFile(ctx context.Context, localPath string, req FileTransferRequest) (FileTransferResult, error) {
	var zero FileTransferResult
	if f == nil {
		return zero, ErrNilTransport
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return zero, err
	}
	if !info.Mode().IsRegular() {
		return zero, fmt.Errorf("file transfer requires a regular file")
	}
	if info.Size() > int64(^uint32(0)) {
		return zero, fmt.Errorf("file too large for current stream framing")
	}

	if req.TransferID == "" {
		req.TransferID, err = newTransferID()
		if err != nil {
			return zero, err
		}
	}
	if req.Name == "" {
		req.Name = filepath.Base(localPath)
	}
	req.Size = info.Size()
	if req.SHA256 == "" {
		sum, err := checksumFile(localPath)
		if err != nil {
			return zero, err
		}
		req.SHA256 = sum
	}
	bodyHeader, err := encodeTransferBodyHeader(req.TransferID)
	if err != nil {
		return zero, err
	}
	if uint64(len(bodyHeader))+uint64(req.Size) > uint64(^uint32(0)) {
		return zero, fmt.Errorf("file too large for current stream framing")
	}

	resp, err := f.open.Request(ctx, req)
	if err != nil {
		return zero, err
	}
	if !resp.Accepted {
		if resp.Error == "" {
			resp.Error = "file transfer rejected"
		}
		return zero, errors.New(resp.Error)
	}

	file, err := os.Open(localPath)
	if err != nil {
		return zero, err
	}
	defer file.Close()

	if err := f.body.Send(ctx, fileTransferBody{
		TransferID: req.TransferID,
		Reader:     file,
		Length:     uint32(req.Size),
	}); err != nil {
		return zero, err
	}

	result, err := f.commit.Request(ctx, FileTransferCommit{TransferID: req.TransferID})
	if err != nil {
		return zero, err
	}
	if !result.OK {
		if result.Error == "" {
			result.Error = "file transfer failed"
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

func (f *FileTransfer) handleOpen(ctx context.Context, req FileTransferRequest) (FileTransferResponse, error) {
	_ = ctx
	resp := FileTransferResponse{TransferID: req.TransferID}
	if req.TransferID == "" {
		resp.Error = "transfer_id is required"
		return resp, nil
	}
	if req.Size < 0 {
		resp.Error = "size must be >= 0"
		return resp, nil
	}

	f.lock.Lock()
	if _, exists := f.incoming[req.TransferID]; exists {
		f.lock.Unlock()
		resp.Error = "transfer already exists"
		return resp, nil
	}
	handler := f.onReceive
	f.lock.Unlock()
	if handler == nil {
		resp.Error = "no file receiver configured"
		return resp, nil
	}

	plan, err := handler(ctx, req)
	if err != nil {
		resp.Error = err.Error()
		return resp, nil
	}
	if plan.DestinationPath == "" {
		resp.Error = "destination path is required"
		return resp, nil
	}

	destDir := filepath.Dir(plan.DestinationPath)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		resp.Error = err.Error()
		return resp, nil
	}
	tmp, err := os.CreateTemp(destDir, "."+filepath.Base(plan.DestinationPath)+".part-*")
	if err != nil {
		resp.Error = err.Error()
		return resp, nil
	}

	incoming := &incomingFileTransfer{
		finalPath: plan.DestinationPath,
		tempPath:  tmp.Name(),
		file:      tmp,
		hash:      sha256.New(),
		expected:  req,
	}

	f.lock.Lock()
	f.incoming[req.TransferID] = incoming
	f.lock.Unlock()

	resp.Accepted = true
	return resp, nil
}

func (f *FileTransfer) handleBody(ctx context.Context, msg *Message) error {
	_ = ctx
	r := msg.Reader()
	if r == nil {
		return ErrNilMessage
	}

	transferID, err := decodeTransferBodyHeader(r)
	if err != nil {
		return err
	}

	f.lock.Lock()
	incoming := f.incoming[transferID]
	f.lock.Unlock()
	if incoming == nil {
		return fmt.Errorf("unknown transfer: %s", transferID)
	}

	incoming.lock.Lock()
	defer incoming.lock.Unlock()

	if incoming.bodyWritten {
		return fmt.Errorf("transfer body already written")
	}
	w := io.MultiWriter(incoming.file, incoming.hash)
	n, err := io.Copy(w, r)
	if err != nil {
		_ = incoming.file.Close()
		return err
	}
	if err := incoming.file.Close(); err != nil {
		return err
	}
	incoming.bytesWritten = n
	incoming.bodyWritten = true
	return nil
}

func (f *FileTransfer) handleCommit(ctx context.Context, req FileTransferCommit) (FileTransferResult, error) {
	_ = ctx
	result := FileTransferResult{TransferID: req.TransferID}

	f.lock.Lock()
	incoming := f.incoming[req.TransferID]
	if incoming != nil {
		delete(f.incoming, req.TransferID)
	}
	f.lock.Unlock()
	if incoming == nil {
		result.Error = "unknown transfer"
		return result, nil
	}

	incoming.lock.Lock()
	defer incoming.lock.Unlock()
	defer func() {
		if !result.OK {
			_ = os.Remove(incoming.tempPath)
		}
	}()

	if !incoming.bodyWritten {
		result.Error = "transfer body not received"
		return result, nil
	}
	if incoming.bytesWritten != incoming.expected.Size {
		result.Error = fmt.Sprintf("size mismatch: got %d want %d", incoming.bytesWritten, incoming.expected.Size)
		return result, nil
	}

	sum := hex.EncodeToString(incoming.hash.Sum(nil))
	if incoming.expected.SHA256 != "" && !strings.EqualFold(sum, incoming.expected.SHA256) {
		result.Error = "checksum mismatch"
		return result, nil
	}

	if err := os.Rename(incoming.tempPath, incoming.finalPath); err != nil {
		result.Error = err.Error()
		return result, nil
	}

	result.OK = true
	result.Size = incoming.bytesWritten
	result.SHA256 = sum
	return result, nil
}

// ResolveSenderPathUnderRoot is appropriate when the sender is allowed to
// suggest a relative destination under a receiver-controlled root.
func ResolveSenderPathUnderRoot(rootDir, requestedPath, fallbackName string) (string, error) {
	if rootDir == "" {
		return "", fmt.Errorf("rootDir is required")
	}
	path := requestedPath
	if path == "" {
		path = fallbackName
	}
	if path == "" {
		return "", fmt.Errorf("requested path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == "" || clean == ".." {
		return "", fmt.Errorf("invalid relative path")
	}
	prefix := ".." + string(os.PathSeparator)
	if strings.HasPrefix(clean, prefix) {
		return "", fmt.Errorf("path traversal is not allowed")
	}

	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(rootAbs, clean)
	rel, err := filepath.Rel(rootAbs, dest)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, prefix) {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	return dest, nil
}

// ResolveHostPathByName is appropriate when the receiver must ignore any
// sender-chosen destination and place the file under its own controlled root.
func ResolveHostPathByName(rootDir, name string) (string, error) {
	if rootDir == "" {
		return "", fmt.Errorf("rootDir is required")
	}
	base := filepath.Base(name)
	if base == "." || base == "" || base == string(os.PathSeparator) {
		return "", fmt.Errorf("name is required")
	}
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(rootAbs, base), nil
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func newTransferID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func encodeTransferBodyHeader(id string) ([]byte, error) {
	if id == "" {
		return nil, fmt.Errorf("transfer_id is required")
	}
	if len(id) > int(^uint16(0)) {
		return nil, fmt.Errorf("transfer_id too large")
	}
	header := make([]byte, 2+len(id))
	binary.BigEndian.PutUint16(header[:2], uint16(len(id)))
	copy(header[2:], id)
	return header, nil
}

func decodeTransferBodyHeader(r io.Reader) (string, error) {
	var lengthBuf [2]byte
	if _, err := io.ReadFull(r, lengthBuf[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(lengthBuf[:])
	if n == 0 {
		return "", fmt.Errorf("transfer_id is required")
	}
	idBuf := make([]byte, n)
	if _, err := io.ReadFull(r, idBuf); err != nil {
		return "", err
	}
	return string(idBuf), nil
}

func bytesReader(b []byte) io.Reader {
	return bytes.NewReader(b)
}
