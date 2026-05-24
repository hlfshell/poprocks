package vsock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type mJSONPayload struct {
	Name string `json:"name"`
}

func readOneMessage(conn net.Conn) (*Message, error) {
	header := make([]byte, headerLength)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint32(header[12:16]))
	raw := make([]byte, headerLength+length)
	copy(raw[:headerLength], header)
	if _, err := io.ReadFull(conn, raw[headerLength:]); err != nil {
		return nil, err
	}
	return ParseBinary(raw)
}

func readOneMessageFromConn(t *testing.T, conn net.Conn) *Message {
	t.Helper()
	msg, err := readOneMessage(conn)
	if err != nil {
		t.Fatalf("parse raw message: %v", err)
	}
	return msg
}

func TestMessengerConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MessengerConfig
		wantErr bool
	}{
		{
			name:    "ack without timeout invalid",
			cfg:     MessengerConfig{RequireAcknowledge: true},
			wantErr: true,
		},
		{
			name:    "negative retries invalid",
			cfg:     MessengerConfig{MaxRetries: -1},
			wantErr: true,
		},
		{
			name:    "size above absolute invalid",
			cfg:     MessengerConfig{MaxMessageSize: AbsoluteMaxMessageSize + 1},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: MessengerConfig{
				RequireAcknowledge:     true,
				Timeout:                time.Second,
				MaxRetries:             1,
				MaxMessageSize:         1024,
				MaxMessageSizeReceived: 2048,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestDefaultMessengerConfig(t *testing.T) {
	cfg := DefaultMessengerConfig()

	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %v, want %v", cfg.Timeout, DefaultTimeout)
	}
	if cfg.MaxRetries != DefaultMaxRetries {
		t.Fatalf("MaxRetries = %d, want %d", cfg.MaxRetries, DefaultMaxRetries)
	}
	if cfg.MaxMessageSize != DefaultMaxMessageSize {
		t.Fatalf("MaxMessageSize = %d, want %d", cfg.MaxMessageSize, DefaultMaxMessageSize)
	}
	if cfg.MaxMessageSizeReceived != DefaultMaxMessageSize {
		t.Fatalf("MaxMessageSizeReceived = %d, want %d", cfg.MaxMessageSizeReceived, DefaultMaxMessageSize)
	}
}

func TestNewMessengerWithConfig(t *testing.T) {
	cfg := MessengerConfig{
		RequireAcknowledge:     true,
		Timeout:                time.Second,
		MaxRetries:             2,
		MaxMessageSize:         1024,
		MaxMessageSizeReceived: 2048,
	}

	m, err := NewMessengerWithConfig(nil, cfg)
	if err != nil {
		t.Fatalf("NewMessengerWithConfig() unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected messenger")
	}
	if m.config != cfg {
		t.Fatalf("config mismatch: got %+v want %+v", m.config, cfg)
	}
}

func TestNewMessengerWithConfigRejectsInvalidConfig(t *testing.T) {
	_, err := NewMessengerWithConfig(nil, MessengerConfig{
		RequireAcknowledge: true,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestMessengerSendRequiresAcknowledge(t *testing.T) {
	senderConn, receiverConn := net.Pipe()
	defer senderConn.Close()
	defer receiverConn.Close()

	sender, err := NewMessengerWithConfig(senderConn, MessengerConfig{
		RequireAcknowledge:     true,
		Timeout:                50 * time.Millisecond,
		MaxRetries:             0,
		MaxMessageSize:         DefaultMaxMessageSize,
		MaxMessageSizeReceived: DefaultMaxMessageSize,
	})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	receiver := NewMessenger(receiverConn)

	received := make(chan struct{}, 1)
	if err := receiver.OnReceive(88, func(context.Context, *Message) error {
		received <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	senderErr := make(chan error, 1)
	receiverErr := make(chan error, 1)
	go func() { senderErr <- sender.Serve(ctx) }()
	go func() { receiverErr <- receiver.Serve(ctx) }()

	if err := sender.Send(ctx, NewMessage(1, 88, []byte("hello"))); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for receiver")
	}

	cancel()
	if err := <-senderErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("sender serve error: %v", err)
	}
	if err := <-receiverErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("receiver serve error: %v", err)
	}
}

func TestMessengerSendRetriesAfterAcknowledgeTimeout(t *testing.T) {
	senderConn, receiverConn := net.Pipe()
	defer senderConn.Close()
	defer receiverConn.Close()

	sender, err := NewMessengerWithConfig(senderConn, MessengerConfig{
		RequireAcknowledge:     true,
		Timeout:                20 * time.Millisecond,
		MaxRetries:             1,
		MaxMessageSize:         DefaultMaxMessageSize,
		MaxMessageSizeReceived: DefaultMaxMessageSize,
	})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		first, err := readOneMessage(receiverConn)
		if err != nil {
			done <- err
			return
		}
		attempts.Add(1)
		if first.ID != 2 || first.Type != 89 {
			done <- errors.New("unexpected first message")
			return
		}

		second, err := readOneMessage(receiverConn)
		if err != nil {
			done <- err
			return
		}
		attempts.Add(1)
		if second.ID != 2 || second.Type != 89 {
			done <- errors.New("unexpected second message")
			return
		}

		if _, err := receiverConn.Write(NewMessage(second.ID, acknowledgeTypeID, nil).Binary()); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	senderErr := make(chan error, 1)
	go func() { senderErr <- sender.Serve(ctx) }()

	if err := sender.Send(ctx, NewMessage(2, 89, []byte("retry"))); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("receiver flow: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried delivery")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}

	cancel()
	if err := <-senderErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("sender serve error: %v", err)
	}
}

func TestMessengerSendFailsWhenAcknowledgeExhausted(t *testing.T) {
	senderConn, receiverConn := net.Pipe()
	defer senderConn.Close()
	defer receiverConn.Close()

	sender, err := NewMessengerWithConfig(senderConn, MessengerConfig{
		RequireAcknowledge:     true,
		Timeout:                20 * time.Millisecond,
		MaxRetries:             1,
		MaxMessageSize:         DefaultMaxMessageSize,
		MaxMessageSizeReceived: DefaultMaxMessageSize,
	})
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		for range 2 {
			msg, err := readOneMessage(receiverConn)
			if err != nil {
				done <- err
				return
			}
			attempts.Add(1)
			if msg.ID != 3 || msg.Type != 90 {
				done <- errors.New("unexpected retried message")
				return
			}
		}
		done <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	senderErr := make(chan error, 1)
	go func() { senderErr <- sender.Serve(ctx) }()

	err = sender.Send(ctx, NewMessage(3, 90, []byte("timeout")))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("receiver flow: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for exhausted retries")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}

	cancel()
	if err := <-senderErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("sender serve error: %v", err)
	}
}

func TestMessengerRequestRejectsDuplicatePendingID(t *testing.T) {
	m := NewMessenger(nil)
	wait, err := m.registerPendingResponse(33, 92)
	if err != nil {
		t.Fatalf("register first response: %v", err)
	}
	defer m.unregisterPendingResponse(33, wait)

	_, err = m.registerPendingResponse(33, 92)
	if !errors.Is(err, ErrDuplicateMessageID) {
		t.Fatalf("expected ErrDuplicateMessageID, got %v", err)
	}
}

func TestMessengerRequestBufferedEndToEnd(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := NewMessenger(clientConn)
	server := NewMessenger(serverConn)

	if err := server.OnReceive(6101, func(ctx context.Context, msg *Message) error {
		body, err := msg.ReadAll()
		if err != nil {
			return err
		}
		return server.Send(ctx, NewMessage(msg.ID, 6102, append([]byte("resp:"), body...)))
	}); err != nil {
		t.Fatalf("register server handler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientErr := make(chan error, 1)
	serverErr := make(chan error, 1)
	go func() { clientErr <- client.Serve(ctx) }()
	go func() { serverErr <- server.Serve(ctx) }()

	resp, err := client.Request(ctx, NewMessage(101, 6101, []byte("hello")), 6102)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	got, err := resp.ReadAll()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(got) != "resp:hello" {
		t.Fatalf("response payload = %q", got)
	}

	cancel()
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server serve error: %v", err)
	}
}

func TestMessengerConcurrentSameTypeRequestsRouteByID(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := NewMessenger(clientConn)
	server := NewMessenger(serverConn)

	if err := server.OnReceive(6201, func(ctx context.Context, msg *Message) error {
		body, err := msg.ReadAll()
		if err != nil {
			return err
		}
		return server.Send(ctx, NewMessage(msg.ID, 6202, append([]byte("ok:"), body...)))
	}); err != nil {
		t.Fatalf("register server handler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientErr := make(chan error, 1)
	serverErr := make(chan error, 1)
	go func() { clientErr <- client.Serve(ctx) }()
	go func() { serverErr <- server.Serve(ctx) }()

	const total = 8
	errs := make(chan error, total)
	for i := range total {
		go func(i int) {
			payload := []byte{byte('a' + i)}
			resp, err := client.Request(ctx, NewMessage(uint64(200+i), 6201, payload), 6202)
			if err != nil {
				errs <- err
				return
			}
			got, err := resp.ReadAll()
			if err != nil {
				errs <- err
				return
			}
			want := append([]byte("ok:"), payload...)
			if !bytes.Equal(got, want) {
				errs <- errors.New("response payload mismatch")
				return
			}
			errs <- nil
		}(i)
	}

	for range total {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for concurrent requests")
		}
	}

	cancel()
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server serve error: %v", err)
	}
}

func TestMessengerResponseTypeMismatchDoesNotResolvePendingRequest(t *testing.T) {
	m := NewMessenger(nil)
	wait, err := m.registerPendingResponse(44, 7002)
	if err != nil {
		t.Fatalf("register pending response: %v", err)
	}
	defer m.unregisterPendingResponse(44, wait)

	resolved, err := m.resolveResponse(NewMessage(44, 7003, []byte("wrong type")))
	if err != nil {
		t.Fatalf("resolve response: %v", err)
	}
	if resolved {
		t.Fatal("response with mismatched type should not resolve")
	}
	select {
	case <-wait:
		t.Fatal("pending response channel should not receive mismatched response")
	default:
	}
}

func TestMessengerSendAndUnknownHandler(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := NewMessenger(c1)
	done := make(chan struct{})
	if err := m.OnUnknown(func(context.Context, *Message) error {
		close(done)
		return nil
	}); err != nil {
		t.Fatalf("set unknown: %v", err)
	}

	msg := NewMessage(1, 1234, []byte("abc"))
	go func() { _ = m.Send(context.Background(), msg) }()
	got := readOneMessageFromConn(t, c2)

	type ackResult struct {
		msg *Message
		err error
	}
	ackRead := make(chan ackResult, 1)
	go func() {
		msg, err := readOneMessage(c2)
		ackRead <- ackResult{msg: msg, err: err}
	}()
	if err := m.handleMessage(context.Background(), got); err != nil {
		t.Fatalf("handle message: %v", err)
	}
	ackResultValue := <-ackRead
	if ackResultValue.err != nil {
		t.Fatalf("read ack: %v", ackResultValue.err)
	}
	ack := ackResultValue.msg
	if ack.Type != acknowledgeTypeID || ack.ID != got.ID {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unknown handler not called")
	}
}

func TestMessengerUnknownMessageWithoutHandlerIsAcked(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := NewMessenger(c1)
	msg := NewMessage(5, 4321, []byte("abc"))
	go func() { _ = m.Send(context.Background(), msg) }()
	got := readOneMessageFromConn(t, c2)

	ackRead := make(chan *Message, 1)
	go func() {
		ack, _ := readOneMessage(c2)
		ackRead <- ack
	}()
	if err := m.handleMessage(context.Background(), got); err != nil {
		t.Fatalf("handle message: %v", err)
	}
	select {
	case ack := <-ackRead:
		if ack == nil || ack.Type != acknowledgeTypeID || ack.ID != got.ID {
			t.Fatalf("unexpected ack: %+v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ack")
	}
}

func TestMessengerHandleMessagePropagatesHandlerError(t *testing.T) {
	m := NewMessenger(nil)
	want := errors.New("handler failed")
	if err := m.OnReceive(8101, func(context.Context, *Message) error {
		return want
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	err := m.handleMessage(context.Background(), NewMessage(1, 8101, nil))
	if !errors.Is(err, want) {
		t.Fatalf("expected handler error, got %v", err)
	}
}

func TestMessengerOnReceiveValidation(t *testing.T) {
	m := NewMessenger(nil)
	if err := m.OnReceive(1, nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("expected ErrNilHandler, got %v", err)
	}
	if err := m.OnReceive(0, func(context.Context, *Message) error { return nil }); !errors.Is(err, ErrInvalidTypeID) {
		t.Fatalf("expected ErrInvalidTypeID, got %v", err)
	}
	if err := m.OnReceive(1, func(context.Context, *Message) error { return nil }); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	if err := m.OnReceive(1, func(context.Context, *Message) error { return nil }); !errors.Is(err, ErrHandlerAlreadyRegistered) {
		t.Fatalf("expected ErrHandlerAlreadyRegistered, got %v", err)
	}
}

func TestMessengerSendValidation(t *testing.T) {
	if err := NewMessenger(nil).Send(context.Background(), NewMessage(1, 2, nil)); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("expected ErrNilTransport, got %v", err)
	}
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if err := NewMessenger(c1).Send(context.Background(), nil); !errors.Is(err, ErrNilMessage) {
		t.Fatalf("expected ErrNilMessage, got %v", err)
	}
}

func TestReadMessageLimitAndServeErrors(t *testing.T) {
	sender, receiver := net.Pipe()
	defer sender.Close()
	defer receiver.Close()

	m := NewMessenger(receiver)
	m.config.MaxMessageSizeReceived = 1
	codec, _ := NewCodec[mJSONPayload](501)
	msg, _ := codec.ToMessage(mJSONPayload{Name: "too-big"})
	go func() { _ = NewMessenger(sender).Send(context.Background(), msg) }()
	if _, err := m.readHeader(); err == nil {
		t.Fatal("expected read size limit error")
	}

	nilTransport := NewMessenger(nil)
	if err := nilTransport.Serve(context.Background()); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("expected ErrNilTransport, got %v", err)
	}

	c1, c2 := net.Pipe()
	serveM := NewMessenger(c1)
	done := make(chan error, 1)
	go func() { done <- serveM.Serve(context.Background()) }()
	_ = c2.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on closed conn, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve did not stop on close")
	}
}

func TestNewCodecWithEncodingInvalidTypeID(t *testing.T) {
	if _, err := NewCodecOfType[mJSONPayload](0, CodecJSON); !errors.Is(err, ErrInvalidTypeID) {
		t.Fatalf("expected ErrInvalidTypeID, got %v", err)
	}
}
