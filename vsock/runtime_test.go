package vsock

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
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
			name: "heartbeat timeout not greater invalid",
			cfg: MessengerConfig{
				Heartbeat:         true,
				HeartbeatInterval: 2 * time.Second,
				HeartbeatTimeout:  1 * time.Second,
			},
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
				Heartbeat:              true,
				HeartbeatHost:          true,
				HeartbeatInterval:      100 * time.Millisecond,
				HeartbeatTimeout:       300 * time.Millisecond,
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
	if cfg.Heartbeat != DefaultHearatbeat {
		t.Fatalf("Heartbeat = %v, want %v", cfg.Heartbeat, DefaultHearatbeat)
	}
	if cfg.HeartbeatHost != DefaultHeartbeatHost {
		t.Fatalf("HeartbeatHost = %v, want %v", cfg.HeartbeatHost, DefaultHeartbeatHost)
	}
	if cfg.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("HeartbeatInterval = %v, want %v", cfg.HeartbeatInterval, DefaultHeartbeatInterval)
	}
	if cfg.HeartbeatTimeout != DefaultHeartbeatTimeout {
		t.Fatalf("HeartbeatTimeout = %v, want %v", cfg.HeartbeatTimeout, DefaultHeartbeatTimeout)
	}
}

func TestNewMessengerWithConfig(t *testing.T) {
	cfg := MessengerConfig{
		RequireAcknowledge:     true,
		Timeout:                time.Second,
		MaxRetries:             2,
		MaxMessageSize:         1024,
		MaxMessageSizeReceived: 2048,
		Heartbeat:              true,
		HeartbeatHost:          false,
		HeartbeatInterval:      100 * time.Millisecond,
		HeartbeatTimeout:       300 * time.Millisecond,
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

func TestMWrapperFlowAndConflicts(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := NewMessenger(c1)
	codec, err := NewCodec[mJSONPayload](9001)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}

	w, err := NewM[mJSONPayload](m, codec)
	if err != nil {
		t.Fatalf("new wrapper: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	w.OnReceive(func(p mJSONPayload) {
		defer wg.Done()
		if p.Name != "test" {
			t.Errorf("unexpected payload: %+v", p)
		}
	})

	incoming, err := codec.ToMessage(mJSONPayload{Name: "test"})
	if err != nil {
		t.Fatalf("encode incoming: %v", err)
	}
	type ackResult struct {
		msg *Message
		err error
	}
	ackRead := make(chan ackResult, 1)
	go func() {
		msg, err := readOneMessage(c2)
		ackRead <- ackResult{msg: msg, err: err}
	}()
	if err := m.handleMessage(context.Background(), incoming); err != nil {
		t.Fatalf("dispatch incoming: %v", err)
	}
	ackResultValue := <-ackRead
	if ackResultValue.err != nil {
		t.Fatalf("read ack: %v", ackResultValue.err)
	}
	ack := ackResultValue.msg
	if ack.Type != acknowledgeTypeID || ack.ID != incoming.ID {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	wait := make(chan struct{})
	go func() { wg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("typed receiver not called")
	}

	sendDone := make(chan error, 1)
	go func() { sendDone <- w.Send(context.Background(), mJSONPayload{Name: "out"}) }()
	out := readOneMessageFromConn(t, c2)
	decoded, err := codec.Decode(out)
	if err != nil {
		t.Fatalf("decode outbound: %v", err)
	}
	if decoded.Name != "out" {
		t.Fatalf("unexpected outbound payload: %+v", decoded)
	}
	if err := <-sendDone; err != nil {
		t.Fatalf("wrapper send error: %v", err)
	}

	if _, err := NewM[mJSONPayload](nil, codec); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("expected ErrNilTransport, got %v", err)
	}
	if _, err := NewM[mJSONPayload](m, nil); !errors.Is(err, ErrNilCodec) {
		t.Fatalf("expected ErrNilCodec, got %v", err)
	}
	if _, err := NewM[mJSONPayload](m, codec); err == nil {
		t.Fatal("expected duplicate type registration error")
	}
}

func TestMessengerServiceEndToEndBidirectional(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := NewMessenger(hostConn)
	client := NewMessenger(clientConn)

	codec, err := NewCodec[mJSONPayload](777)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	hostM, err := NewM[mJSONPayload](host, codec)
	if err != nil {
		t.Fatalf("new host wrapper: %v", err)
	}
	clientM, err := NewM[mJSONPayload](client, codec)
	if err != nil {
		t.Fatalf("new client wrapper: %v", err)
	}

	hostRecv := make(chan string, 1)
	clientRecv := make(chan string, 1)
	hostM.OnReceive(func(p mJSONPayload) { hostRecv <- p.Name })
	clientM.OnReceive(func(p mJSONPayload) { clientRecv <- p.Name })

	ctx, cancel := context.WithCancel(context.Background())
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	if err := hostM.Send(ctx, mJSONPayload{Name: "from-host"}); err != nil {
		t.Fatalf("host send: %v", err)
	}
	if err := clientM.Send(ctx, mJSONPayload{Name: "from-client"}); err != nil {
		t.Fatalf("client send: %v", err)
	}

	select {
	case got := <-hostRecv:
		if got != "from-client" {
			t.Fatalf("host got wrong payload: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for host receive")
	}
	select {
	case got := <-clientRecv:
		if got != "from-host" {
			t.Fatalf("client got wrong payload: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for client receive")
	}

	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
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
	if _, err := m.readMessage(); err == nil {
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

func TestHeartbeatBehaviorAndHelpers(t *testing.T) {
	m := NewMessenger(nil)
	m.config.Heartbeat = false
	m.config.HeartbeatHost = true
	m.StartHeartbeat(context.Background())
	if m.heartbeatStarted {
		t.Fatal("heartbeat should not start when disabled")
	}
	if got := m.snapshotHealth(); got != nil {
		t.Fatalf("expected nil health by default, got %#v", got)
	}
	m.OnHeartbeatHealth(func() map[string]any { return map[string]any{"ok": true} })
	if got := m.snapshotHealth(); got == nil || got["ok"] != true {
		t.Fatalf("unexpected heartbeat health callback result: %#v", got)
	}

	m.config.HeartbeatInterval = 0
	m.config.HeartbeatTimeout = 0
	if m.heartbeatInterval() != DefaultHeartbeatInterval {
		t.Fatal("expected default heartbeat interval")
	}
	if m.heartbeatTimeout() != DefaultHeartbeatTimeout {
		t.Fatal("expected default heartbeat timeout")
	}

	m.setHeartbeatStatus(HeartbeatStatusError)
	if m.HeartbeatState().Status != HeartbeatStatusError {
		t.Fatal("expected heartbeat status update")
	}
	m.heartbeatStarted = true
	m.stopHeartbeat()
	if m.heartbeatStarted {
		t.Fatal("expected heartbeat stop to clear started flag")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	m.heartbeatStarted = true
	m.heartbeatLoop(cancelled)
	if m.heartbeatStarted {
		t.Fatal("expected cancelled heartbeat loop to stop")
	}
}

func TestHeartbeatClientHostMessageFlow(t *testing.T) {
	clientConn, hostConn := net.Pipe()
	defer clientConn.Close()
	defer hostConn.Close()

	client := NewMessenger(clientConn)
	client.config.Heartbeat = true
	client.config.HeartbeatHost = false

	host := NewMessenger(hostConn)
	host.config.Heartbeat = true
	host.config.HeartbeatHost = true

	req := heartbeatPayload{SentAt: time.Now(), Status: HeartbeatStatusOK}
	reqMsg, err := client.newHeartbeatMessage(req)
	if err != nil {
		t.Fatalf("new heartbeat message: %v", err)
	}
	go func() { _ = client.handleHeartbeatMessage(context.Background(), reqMsg) }()
	resp := readOneMessageFromConn(t, hostConn)
	respPayload, err := resp.ReadAll()
	if err != nil {
		t.Fatalf("read heartbeat payload: %v", err)
	}

	var rp heartbeatPayload
	if err := msgpack.Unmarshal(respPayload, &rp); err != nil {
		t.Fatalf("unmarshal heartbeat response: %v", err)
	}
	if rp.Status != HeartbeatStatusOK {
		t.Fatalf("unexpected heartbeat status: %d", rp.Status)
	}

	done := make(chan struct{})
	host.hbLock.Lock()
	host.heartbeatPending = done
	host.hbLock.Unlock()
	if err := host.handleHeartbeatMessage(context.Background(), resp); err != nil {
		t.Fatalf("host handle heartbeat response: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("host pending heartbeat was not resolved")
	}
}

func TestNewCodecWithEncodingInvalidTypeID(t *testing.T) {
	if _, err := NewCodecOfType[mJSONPayload](0, CodecJSON); !errors.Is(err, ErrInvalidTypeID) {
		t.Fatalf("expected ErrInvalidTypeID, got %v", err)
	}
}
