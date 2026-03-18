package vsock

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

type mJSONPayload struct {
	Name string `json:"name"`
}

func readOneMessageFromConn(t *testing.T, conn net.Conn) *Message {
	t.Helper()
	header := make([]byte, headerLength)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatalf("read header: %v", err)
	}
	length := int(binary.BigEndian.Uint32(header[12:16]))
	raw := make([]byte, headerLength+length)
	copy(raw[:headerLength], header)
	if _, err := io.ReadFull(conn, raw[headerLength:]); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	msg, err := ParseBinary(raw)
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
			name: "ack without timeout invalid",
			cfg:  MessengerConfig{RequireAcknowledge: true},
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
				RequireAcknowledge: true,
				Timeout:            time.Second,
				MaxRetries:         1,
				MaxMessageSize:     1024,
				MaxMessageSizeReceived: 2048,
				Heartbeat:          true,
				HeartbeatHost:      true,
				HeartbeatInterval:  100 * time.Millisecond,
				HeartbeatTimeout:   300 * time.Millisecond,
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

	if err := m.handleMessage(context.Background(), got); err != nil {
		t.Fatalf("handle message: %v", err)
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
	w.OnReceive(func(p mJSONPayload) error {
		defer wg.Done()
		if p.Name != "test" {
			t.Fatalf("unexpected payload: %+v", p)
		}
		return nil
	})

	incoming, err := codec.ToMessage(mJSONPayload{Name: "test"})
	if err != nil {
		t.Fatalf("encode incoming: %v", err)
	}
	if err := m.handleMessage(context.Background(), incoming); err != nil {
		t.Fatalf("dispatch incoming: %v", err)
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

	if _, err := NewM[mJSONPayload](nil, nil); err == nil {
		t.Fatal("expected nil messenger error")
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
	hostM.OnReceive(func(p mJSONPayload) error { hostRecv <- p.Name; return nil })
	clientM.OnReceive(func(p mJSONPayload) error { clientRecv <- p.Name; return nil })

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
