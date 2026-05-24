package common

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hlfshell/poprocks/vsock"
)

func TestFileTransferHostToGuestUsesReceiverControlledRoot(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	defer hostConn.Close()
	defer guestConn.Close()

	host := vsock.NewMessenger(hostConn)
	guest := vsock.NewMessenger(guestConn)

	hostTransfer, err := NewFileTransfer(host)
	if err != nil {
		t.Fatalf("new host transfer: %v", err)
	}
	guestTransfer, err := NewFileTransfer(guest)
	if err != nil {
		t.Fatalf("new guest transfer: %v", err)
	}

	guestRoot := t.TempDir()
	guestTransfer.OnReceive(func(ctx context.Context, req FileTransferRequest) (FileTransferPlan, error) {
		dest, err := ResolveSenderPathUnderRoot(guestRoot, req.Destination, req.Name)
		if err != nil {
			return FileTransferPlan{}, err
		}
		return FileTransferPlan{DestinationPath: dest}, nil
	})

	sourcePath := filepath.Join(t.TempDir(), "host.txt")
	sourceBody := []byte("host-to-guest payload")
	if err := os.WriteFile(sourcePath, sourceBody, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errChans := serveMessengers(ctx, host, guest)

	result, err := hostTransfer.SendFile(ctx, sourcePath, FileTransferRequest{
		Destination: filepath.Join("inbox", "guest.txt"),
	})
	if err != nil {
		t.Fatalf("send file: %v", err)
	}
	if !result.OK {
		t.Fatalf("transfer result not OK: %+v", result)
	}

	finalPath := filepath.Join(guestRoot, "inbox", "guest.txt")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(sourceBody) {
		t.Fatalf("destination body = %q, want %q", got, sourceBody)
	}

	cancel()
	waitServe(t, errChans...)
}

func TestFileTransferGuestToHostIgnoresSenderDestination(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	defer hostConn.Close()
	defer guestConn.Close()

	host := vsock.NewMessenger(hostConn)
	guest := vsock.NewMessenger(guestConn)

	hostTransfer, err := NewFileTransfer(host)
	if err != nil {
		t.Fatalf("new host transfer: %v", err)
	}
	guestTransfer, err := NewFileTransfer(guest)
	if err != nil {
		t.Fatalf("new guest transfer: %v", err)
	}

	hostRoot := t.TempDir()
	hostTransfer.OnReceive(func(ctx context.Context, req FileTransferRequest) (FileTransferPlan, error) {
		dest, err := ResolveHostPathByName(hostRoot, req.Name)
		if err != nil {
			return FileTransferPlan{}, err
		}
		return FileTransferPlan{DestinationPath: dest}, nil
	})

	sourcePath := filepath.Join(t.TempDir(), "guest.txt")
	sourceBody := []byte("guest-to-host payload")
	if err := os.WriteFile(sourcePath, sourceBody, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errChans := serveMessengers(ctx, host, guest)

	result, err := guestTransfer.SendFile(ctx, sourcePath, FileTransferRequest{
		Name:        "artifact.txt",
		Destination: "../../host-should-ignore-this.txt",
	})
	if err != nil {
		t.Fatalf("send file: %v", err)
	}
	if !result.OK {
		t.Fatalf("transfer result not OK: %+v", result)
	}

	finalPath := filepath.Join(hostRoot, "artifact.txt")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read host destination: %v", err)
	}
	if string(got) != string(sourceBody) {
		t.Fatalf("destination body = %q, want %q", got, sourceBody)
	}
	if _, err := os.Stat(filepath.Join(hostRoot, "host-should-ignore-this.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected sender-controlled destination file state: %v", err)
	}

	cancel()
	waitServe(t, errChans...)
}

func TestResolveSenderPathUnderRootRejectsTraversal(t *testing.T) {
	root := t.TempDir()

	tests := []string{
		"../evil.txt",
		filepath.Join("..", "nested", "evil.txt"),
	}

	for _, path := range tests {
		if _, err := ResolveSenderPathUnderRoot(root, path, ""); err == nil {
			t.Fatalf("expected traversal rejection for %q", path)
		}
	}
}

func TestFileTransferChecksumMismatchRemovesTempFile(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	defer hostConn.Close()
	defer guestConn.Close()

	host := vsock.NewMessenger(hostConn)
	guest := vsock.NewMessenger(guestConn)

	hostTransfer, err := NewFileTransfer(host)
	if err != nil {
		t.Fatalf("new host transfer: %v", err)
	}
	guestTransfer, err := NewFileTransfer(guest)
	if err != nil {
		t.Fatalf("new guest transfer: %v", err)
	}

	guestRoot := t.TempDir()
	guestTransfer.OnReceive(func(ctx context.Context, req FileTransferRequest) (FileTransferPlan, error) {
		dest, err := ResolveSenderPathUnderRoot(guestRoot, req.Destination, req.Name)
		if err != nil {
			return FileTransferPlan{}, err
		}
		return FileTransferPlan{DestinationPath: dest}, nil
	})

	sourcePath := filepath.Join(t.TempDir(), "host.txt")
	if err := os.WriteFile(sourcePath, []byte("checksum payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errChans := serveMessengers(ctx, host, guest)

	_, err = hostTransfer.SendFile(ctx, sourcePath, FileTransferRequest{
		Destination: "bad.txt",
		SHA256:      strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(guestRoot, "bad.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing final file, got %v", err)
	}
	entries, err := os.ReadDir(guestRoot)
	if err != nil {
		t.Fatalf("read guest root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected temp cleanup, found %d entries", len(entries))
	}

	cancel()
	waitServe(t, errChans...)
}

func serveMessengers(ctx context.Context, messengers ...*vsock.Messenger) []chan error {
	errs := make([]chan error, 0, len(messengers))
	for _, messenger := range messengers {
		errCh := make(chan error, 1)
		errs = append(errs, errCh)
		go func(m *vsock.Messenger, ch chan error) {
			ch <- m.Serve(ctx)
		}(messenger, errCh)
	}
	return errs
}

func waitServe(t *testing.T, errChans ...chan error) {
	t.Helper()
	for _, errCh := range errChans {
		err := <-errCh
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("serve error: %v", err)
		}
	}
}
