package vsock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestMessengerSendStreamBidirectional(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := NewMessenger(hostConn)
	client := NewMessenger(clientConn)

	host.config.MaxMessageSize = 8 * 1024 * 1024
	host.config.MaxMessageSizeReceived = 8 * 1024 * 1024
	client.config.MaxMessageSize = 8 * 1024 * 1024
	client.config.MaxMessageSizeReceived = 8 * 1024 * 1024

	hostPayload := bytes.Repeat([]byte("h"), 5*1024*1024)
	clientPayload := bytes.Repeat([]byte("c"), 5*1024*1024)

	hostReceived := make(chan []byte, 1)
	clientReceived := make(chan []byte, 1)

	if err := host.OnReceive(9201, func(ctx context.Context, msg *Message) error {
		_ = ctx
		b, err := msg.ReadAll()
		if err != nil {
			return err
		}
		hostReceived <- b
		return nil
	}); err != nil {
		t.Fatalf("register host stream handler: %v", err)
	}

	if err := client.OnReceive(9201, func(ctx context.Context, msg *Message) error {
		_ = ctx
		b, err := msg.ReadAll()
		if err != nil {
			return err
		}
		clientReceived <- b
		return nil
	}); err != nil {
		t.Fatalf("register client stream handler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	if _, err := host.SendStream(ctx, 9201, uint32(len(hostPayload)), bytes.NewReader(hostPayload)); err != nil {
		t.Fatalf("host send stream: %v", err)
	}
	if _, err := client.SendStream(ctx, 9201, uint32(len(clientPayload)), bytes.NewReader(clientPayload)); err != nil {
		t.Fatalf("client send stream: %v", err)
	}

	select {
	case got := <-hostReceived:
		if !bytes.Equal(got, clientPayload) {
			t.Fatal("host received stream payload mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for host stream receive")
	}

	select {
	case got := <-clientReceived:
		if !bytes.Equal(got, hostPayload) {
			t.Fatal("client received stream payload mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for client stream receive")
	}

	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestMAutoStreamCodecDetectsReaderPayload(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	sender := NewMessenger(c1)
	receiver := NewMessenger(c2)
	sender.config.MaxMessageSize = 8 * 1024 * 1024
	receiver.config.MaxMessageSizeReceived = 8 * 1024 * 1024

	codec, err := NewCodec[ReaderPayload](9501)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	wrapper, err := NewM[ReaderPayload](sender, codec)
	if err != nil {
		t.Fatalf("new wrapper: %v", err)
	}

	payload := bytes.Repeat([]byte("z"), 2*1024*1024+123)
	got := make(chan []byte, 1)
	if err := receiver.OnReceive(codec.TypeID(), func(ctx context.Context, msg *Message) error {
		_ = ctx
		b, err := msg.ReadAll()
		if err != nil {
			return err
		}
		got <- b
		return nil
	}); err != nil {
		t.Fatalf("on stream: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- receiver.Serve(ctx) }()

	if err := wrapper.Send(ctx, ReaderPayload{
		Reader: bytes.NewBuffer(payload),
		Length: uint32(len(payload)),
	}); err != nil {
		t.Fatalf("auto stream send: %v", err)
	}

	select {
	case b := <-got:
		if !bytes.Equal(b, payload) {
			t.Fatal("auto stream payload mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for auto stream payload")
	}

	cancel()
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("serve error: %v", err)
	}
}

func TestMStreamCodecRequiresStreamSourcePayload(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	sender := NewMessenger(c1)
	_ = NewMessenger(c2)

	codec, err := NewCodecOfType[[]byte](9502, CodecStream)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	wrapper, err := NewM[[]byte](sender, codec)
	if err != nil {
		t.Fatalf("new wrapper: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := wrapper.Send(ctx, []byte("hello-stream-bytes")); err == nil {
		t.Fatal("expected stream-source error")
	}
}

func TestMessengerStreamWriteToDisk(t *testing.T) {
	senderConn, receiverConn := net.Pipe()
	defer senderConn.Close()
	defer receiverConn.Close()

	sender := NewMessenger(senderConn)
	receiver := NewMessenger(receiverConn)
	sender.config.MaxMessageSize = 8 * 1024 * 1024
	receiver.config.MaxMessageSizeReceived = 8 * 1024 * 1024

	payload := bytes.Repeat([]byte("bin"), 1024*1024)
	outFile, err := os.CreateTemp(t.TempDir(), "vsock-stream-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer outFile.Close()

	done := make(chan error, 1)
	if err := receiver.OnReceive(9301, func(ctx context.Context, msg *Message) error {
		_ = ctx
		if _, err := outFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if _, err := msg.WriteTo(outFile); err != nil {
			return err
		}
		done <- nil
		return nil
	}); err != nil {
		t.Fatalf("register stream handler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- receiver.Serve(ctx) }()

	if _, err := sender.SendStream(ctx, 9301, uint32(len(payload)), bytes.NewReader(payload)); err != nil {
		t.Fatalf("send stream: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream handler error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for streamed file write")
	}

	if _, err := outFile.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek file start: %v", err)
	}
	got, err := io.ReadAll(outFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("disk payload mismatch")
	}

	cancel()
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("serve error: %v", err)
	}
}

func TestMessengerSendStreamShortReader(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := NewMessenger(c1)
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, headerLength+5)
		_, err := io.ReadFull(c2, buf)
		done <- err
	}()

	_, err := m.SendStream(context.Background(), 9401, 16, bytes.NewReader([]byte("short")))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("peer read failed: %v", err)
	}
}
