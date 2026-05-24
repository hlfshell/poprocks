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
	if err := hostM.OnReceive(func(ctx context.Context, p mJSONPayload) error {
		_ = ctx
		defer wg.Done()
		if p.Name != "test" {
			t.Errorf("unexpected payload: %+v", p)
		}
		return nil
	}); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

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
	if err := hostM.OnReceive(func(ctx context.Context, p mJSONPayload) error {
		_ = ctx
		hostRecv <- p.Name
		return nil
	}); err != nil {
		t.Fatalf("register host receiver: %v", err)
	}
	if err := clientM.OnReceive(func(ctx context.Context, p mJSONPayload) error {
		_ = ctx
		clientRecv <- p.Name
		return nil
	}); err != nil {
		t.Fatalf("register client receiver: %v", err)
	}

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

	if err := server.OnRequest(func(ctx context.Context, req requestPayload) (responsePayload, error) {
		_ = ctx
		return responsePayload{Reply: "hello " + req.Name}, nil
	}); err != nil {
		t.Fatalf("register request receiver: %v", err)
	}

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

func TestRequestResponseStreamEndToEnd(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	reqCodec, err := vsock.NewCodec[vsock.ReaderPayload](1211)
	if err != nil {
		t.Fatalf("request codec: %v", err)
	}
	respCodec, err := vsock.NewCodec[vsock.ReaderPayload](1212)
	if err != nil {
		t.Fatalf("response codec: %v", err)
	}

	server, err := NewR[vsock.ReaderPayload, vsock.ReaderPayload](host, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new server request wrapper: %v", err)
	}
	clientReq, err := NewR[vsock.ReaderPayload, vsock.ReaderPayload](client, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new client request wrapper: %v", err)
	}

	if err := server.OnRequest(func(ctx context.Context, req vsock.ReaderPayload) (vsock.ReaderPayload, error) {
		_ = ctx
		body, err := io.ReadAll(req.Reader)
		if err != nil {
			return vsock.ReaderPayload{}, err
		}
		out := append([]byte("echo:"), body...)
		return vsock.ReaderPayload{
			Reader: bytes.NewReader(out),
			Length: uint32(len(out)),
		}, nil
	}); err != nil {
		t.Fatalf("register stream request receiver: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	input := []byte("stream request body")
	resp, err := clientReq.Request(ctx, vsock.ReaderPayload{
		Reader: bytes.NewReader(input),
		Length: uint32(len(input)),
	})
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	got, err := io.ReadAll(resp.Reader)
	if err != nil {
		t.Fatalf("read stream response: %v", err)
	}
	if want := append([]byte("echo:"), input...); !bytes.Equal(got, want) {
		t.Fatalf("unexpected stream response: got=%q want=%q", got, want)
	}

	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestRequestResponseStreamRequestBufferedResponseEndToEnd(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	reqCodec, err := vsock.NewCodec[vsock.ReaderPayload](1213)
	if err != nil {
		t.Fatalf("request codec: %v", err)
	}
	respCodec, err := vsock.NewCodecOfType[responsePayload](1214, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("response codec: %v", err)
	}

	server, err := NewR[vsock.ReaderPayload, responsePayload](host, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new server request wrapper: %v", err)
	}
	clientReq, err := NewR[vsock.ReaderPayload, responsePayload](client, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new client request wrapper: %v", err)
	}

	if err := server.OnRequest(func(ctx context.Context, req vsock.ReaderPayload) (responsePayload, error) {
		_ = ctx
		body, err := io.ReadAll(req.Reader)
		if err != nil {
			return responsePayload{}, err
		}
		return responsePayload{Reply: "read " + string(body)}, nil
	}); err != nil {
		t.Fatalf("register stream request receiver: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	input := []byte("stream input")
	resp, err := clientReq.Request(ctx, vsock.ReaderPayload{
		Reader: bytes.NewReader(input),
		Length: uint32(len(input)),
	})
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	if resp.Reply != "read stream input" {
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

func TestRequestResponseBufferedRequestStreamResponseEndToEnd(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	reqCodec, err := vsock.NewCodecOfType[requestPayload](1215, vsock.CodecJSON)
	if err != nil {
		t.Fatalf("request codec: %v", err)
	}
	respCodec, err := vsock.NewCodec[vsock.ReaderPayload](1216)
	if err != nil {
		t.Fatalf("response codec: %v", err)
	}

	server, err := NewR[requestPayload, vsock.ReaderPayload](host, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new server request wrapper: %v", err)
	}
	clientReq, err := NewR[requestPayload, vsock.ReaderPayload](client, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("new client request wrapper: %v", err)
	}

	if err := server.OnRequest(func(ctx context.Context, req requestPayload) (vsock.ReaderPayload, error) {
		_ = ctx
		out := []byte("stream reply to " + req.Name)
		return vsock.ReaderPayload{
			Reader: bytes.NewReader(out),
			Length: uint32(len(out)),
		}, nil
	}); err != nil {
		t.Fatalf("register request receiver: %v", err)
	}

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
	got, err := io.ReadAll(resp.Reader)
	if err != nil {
		t.Fatalf("read stream response: %v", err)
	}
	if string(got) != "stream reply to vm" {
		t.Fatalf("unexpected stream response: %q", got)
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
	r, err := NewR[requestPayload, responsePayload](m, reqCodec, respCodec)
	if err != nil {
		t.Fatalf("first NewR failed: %v", err)
	}
	if _, err := NewR[requestPayload, responsePayload](m, reqCodec, respCodec); err == nil {
		t.Fatal("expected duplicate request type registration error")
	}
	if err := r.OnRequest(func(context.Context, requestPayload) (responsePayload, error) {
		return responsePayload{}, nil
	}); err != nil {
		t.Fatalf("register request receiver: %v", err)
	}
	if err := r.OnRequest(func(context.Context, requestPayload) (responsePayload, error) {
		return responsePayload{}, nil
	}); !errors.Is(err, vsock.ErrHandlerAlreadyRegistered) {
		t.Fatalf("expected ErrHandlerAlreadyRegistered for second request receiver, got %v", err)
	}
	if !r.RemoveReceiver() {
		t.Fatal("expected request receiver removal to succeed")
	}
	if r.RemoveReceiver() {
		t.Fatal("expected second request receiver removal to fail")
	}
	if err := r.OnRequest(func(context.Context, requestPayload) (responsePayload, error) {
		return responsePayload{}, nil
	}); err != nil {
		t.Fatalf("register request receiver after removal: %v", err)
	}
	if !r.RemoveReceiver() {
		t.Fatal("expected request receiver removal to succeed")
	}
	if err := r.OnRequest(nil); !errors.Is(err, vsock.ErrNilHandler) {
		t.Fatalf("expected ErrNilHandler for nil request receiver, got %v", err)
	}
}

func TestMReceiverRemoval(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	codec, err := vsock.NewCodec[mJSONPayload](1450)
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

	active := make(chan string, 1)
	if err := hostM.OnReceive(func(ctx context.Context, p mJSONPayload) error {
		_ = ctx
		t.Fatalf("removed receiver was called with %q", p.Name)
		return nil
	}); err != nil {
		t.Fatalf("register removed receiver: %v", err)
	}
	if !hostM.RemoveReceiver() {
		t.Fatal("expected receiver removal to succeed")
	}
	if hostM.RemoveReceiver() {
		t.Fatal("expected second receiver removal to fail")
	}
	if err := hostM.OnReceive(func(ctx context.Context, p mJSONPayload) error {
		_ = ctx
		active <- p.Name
		return nil
	}); err != nil {
		t.Fatalf("register active receiver: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	if err := clientM.Send(ctx, mJSONPayload{Name: "kept"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case got := <-active:
		if got != "kept" {
			t.Fatalf("active receiver got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for active receiver")
	}
	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestMRejectsMultipleReceivers(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	messenger := vsock.NewMessenger(c1)
	codec, err := vsock.NewCodec[mJSONPayload](1457)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	wrapper, err := NewM[mJSONPayload](messenger, codec)
	if err != nil {
		t.Fatalf("new wrapper: %v", err)
	}

	if err := wrapper.OnReceive(func(context.Context, mJSONPayload) error { return nil }); err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	if err := wrapper.OnReceive(func(context.Context, mJSONPayload) error { return nil }); !errors.Is(err, vsock.ErrHandlerAlreadyRegistered) {
		t.Fatalf("expected ErrHandlerAlreadyRegistered for second receiver, got %v", err)
	}
	if !wrapper.RemoveReceiver() {
		t.Fatal("expected receiver removal to succeed")
	}
	if wrapper.RemoveReceiver() {
		t.Fatal("expected second receiver removal to fail")
	}
	if err := wrapper.OnReceive(func(context.Context, mJSONPayload) error { return nil }); err != nil {
		t.Fatalf("register receiver after removal: %v", err)
	}
}

func TestMRejectsNilHandlers(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	messenger := vsock.NewMessenger(c1)
	mCodec, err := vsock.NewCodec[mJSONPayload](1454)
	if err != nil {
		t.Fatalf("message codec: %v", err)
	}
	m, err := NewM[mJSONPayload](messenger, mCodec)
	if err != nil {
		t.Fatalf("new message wrapper: %v", err)
	}
	if err := m.OnReceive(nil); !errors.Is(err, vsock.ErrNilHandler) {
		t.Fatalf("expected ErrNilHandler for nil receiver, got %v", err)
	}
	if err := (*M[mJSONPayload])(nil).OnReceive(func(context.Context, mJSONPayload) error { return nil }); !errors.Is(err, vsock.ErrNilHandler) {
		t.Fatalf("expected ErrNilHandler for nil M, got %v", err)
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
		t.Fatalf("new sender wrapper: %v", err)
	}
	receiverWrapper, err := NewM[vsock.ReaderPayload](receiver, codec)
	if err != nil {
		t.Fatalf("new receiver wrapper: %v", err)
	}

	got := make(chan []byte, 1)
	if err := receiverWrapper.OnReceive(func(ctx context.Context, payload vsock.ReaderPayload) error {
		_ = ctx
		b, err := io.ReadAll(payload.Reader)
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
