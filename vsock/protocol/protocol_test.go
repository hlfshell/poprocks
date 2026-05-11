package protocol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hlfshell/poprocks/vsock"
)

type mJSONPayload struct {
	Name string `json:"name"`
}

type requestPayload struct {
	Name string `json:"name"`
}

type responsePayload struct {
	Reply string `json:"reply"`
}

func TestMWrapperFlowAndConflicts(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	codec, err := vsock.NewCodec[mJSONPayload](9001)
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

	var wg sync.WaitGroup
	wg.Add(1)
	hostM.OnReceive(func(p mJSONPayload) {
		defer wg.Done()
		if p.Name != "test" {
			t.Errorf("unexpected payload: %+v", p)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	if err := clientM.Send(ctx, mJSONPayload{Name: "test"}); err != nil {
		t.Fatalf("send incoming: %v", err)
	}

	wait := make(chan struct{})
	go func() { wg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-time.After(time.Second):
		t.Fatal("typed receiver not called")
	}

	if _, err := NewM[mJSONPayload](nil, codec); !errors.Is(err, vsock.ErrNilTransport) {
		t.Fatalf("expected ErrNilTransport, got %v", err)
	}
	if _, err := NewM[mJSONPayload](host, nil); !errors.Is(err, vsock.ErrNilCodec) {
		t.Fatalf("expected ErrNilCodec, got %v", err)
	}
	if _, err := NewM[mJSONPayload](host, codec); err == nil {
		t.Fatal("expected duplicate type registration error")
	}

	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestMessengerServiceEndToEndBidirectional(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	codec, err := vsock.NewCodec[mJSONPayload](777)
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

func TestRequestResponseEndToEnd(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	reqCodec, err := vsock.NewCodecOfType[requestPayload](1201, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("request codec: %v", err)
	}
	respCodec, err := vsock.NewCodecOfType[responsePayload](1202, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("response codec: %v", err)
	}

	server, err := NewR[requestPayload, responsePayload](host, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new server request wrapper: %v", err)
	}
	clientReq, err := NewR[requestPayload, responsePayload](client, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new client request wrapper: %v", err)
	}

	server.OnRequest(func(ctx context.Context, req requestPayload) (responsePayload, error) {
		_ = ctx
		return responsePayload{Reply: "hello " + req.Name}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	resp, err := clientReq.Request(ctx, requestPayload{Name: "vm"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.Reply != "hello vm" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestRequestResponseTimeout(t *testing.T) {
	senderConn, receiverConn := net.Pipe()
	defer senderConn.Close()
	defer receiverConn.Close()

	client := vsock.NewMessenger(senderConn)
	reqCodec, err := vsock.NewCodecOfType[requestPayload](1301, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("request codec: %v", err)
	}
	respCodec, err := vsock.NewCodecOfType[responsePayload](1302, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("response codec: %v", err)
	}

	clientReq, err := NewR[requestPayload, responsePayload](client, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new request wrapper: %v", err)
	}

	drained := make(chan error, 1)
	go func() {
		_, err := readOneMessage(receiverConn)
		drained <- err
	}()

	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	clientErr := make(chan error, 1)
	go func() { clientErr <- client.Serve(serveCtx) }()

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer reqCancel()
	_, err = clientReq.Request(reqCtx, requestPayload{Name: "timeout"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if err := <-drained; err != nil {
		t.Fatalf("peer drain error: %v", err)
	}

	serveCancel()
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestNewRRejectsNilAndDuplicate(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := vsock.NewMessenger(c1)
	reqCodec, err := vsock.NewCodecOfType[requestPayload](1401, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("request codec: %v", err)
	}
	respCodec, err := vsock.NewCodecOfType[responsePayload](1402, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("response codec: %v", err)
	}

	if _, err := NewR[requestPayload, responsePayload](nil, reqCodec, respCodec); !errors.Is(err, vsock.ErrNilTransport) {
		t.Fatalf("expected ErrNilTransport, got %v", err)
	}
	if _, err := NewR[requestPayload, responsePayload](m, nil, respCodec); !errors.Is(err, vsock.ErrNilCodec) {
		t.Fatalf("expected ErrNilCodec for nil request codec, got %v", err)
	}
	if _, err := NewR[requestPayload, responsePayload](m, reqCodec, nil); !errors.Is(err, vsock.ErrNilCodec) {
		t.Fatalf("expected ErrNilCodec for nil response codec, got %v", err)
	}
	if _, err := NewR[requestPayload, responsePayload](m, reqCodec, respCodec); err != nil {
		t.Fatalf("first NewR failed: %v", err)
	}
	if _, err := NewR[requestPayload, responsePayload](m, reqCodec, respCodec); err == nil {
		t.Fatal("expected duplicate request type registration error")
	}
}

func TestMAutoStreamCodecDetectsReaderPayload(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	sender := vsock.NewMessenger(c1)
	receiver := vsock.NewMessenger(c2)

	payload := bytes.Repeat([]byte("z"), 2*1024*1024+123)
	codec, err := vsock.NewCodec[vsock.ReaderPayload](9501)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	wrapper, err := NewM[vsock.ReaderPayload](sender, codec)
	if err != nil {
		t.Fatalf("new wrapper: %v", err)
	}

	got := make(chan []byte, 1)
	if err := receiver.OnReceive(codec.TypeID(), func(ctx context.Context, msg *vsock.Message) error {
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

	if err := wrapper.Send(ctx, vsock.ReaderPayload{
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

	sender := vsock.NewMessenger(c1)
	_ = vsock.NewMessenger(c2)

	codec, err := vsock.NewCodecOfType[[]byte](9502, vsock.CodecStream)
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

func readOneMessage(conn net.Conn) (*vsock.Message, error) {
	const headerLength = 16
	header := make([]byte, headerLength)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := int(header[12])<<24 | int(header[13])<<16 | int(header[14])<<8 | int(header[15])
	raw := make([]byte, headerLength+length)
	copy(raw[:headerLength], header)
	if _, err := io.ReadFull(conn, raw[headerLength:]); err != nil {
		return nil, err
	}
	return vsock.ParseBinary(raw)
}
