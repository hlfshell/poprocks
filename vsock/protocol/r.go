package protocol

import (
	"context"
	"sync"

	"github.com/hlfshell/poprocks/vsock"
)

/*
R[Req, Resp] is a request/response wrapper. for vsock messaging. It allows you
to define a request and expected response types so that more complex messaging
patterns can be built, such as more useful function calling.

A Request/Response allows only one paired requester and responder, unlike
messages which can follow a fan out strategy. The codecs for the
request/response can handle streaming/non streaming payloads as the pair expect.
*/
type R[Req any, Resp any] struct {
	lock          sync.RWMutex
	requestCodec  *vsock.Codec[Req]
	responseCodec *vsock.Codec[Resp]
	messenger     *vsock.Messenger
	handler       func(context.Context, Req) (Resp, error)
}

func NewR[Req any, Resp any](messenger *vsock.Messenger, requestCodec *vsock.Codec[Req], responseCodec *vsock.Codec[Resp]) (*R[Req, Resp], error) {
	if messenger == nil {
		return nil, vsock.ErrNilTransport
	}
	if requestCodec == nil || responseCodec == nil {
		return nil, vsock.ErrNilCodec
	}

	r := &R[Req, Resp]{
		requestCodec:  requestCodec,
		responseCodec: responseCodec,
		messenger:     messenger,
	}

	if err := messenger.OnReceive(requestCodec.TypeID(), func(ctx context.Context, streamMsg *vsock.Message) error {
		req, err := requestCodec.Decode(streamMsg)
		if err != nil {
			return err
		}

		r.lock.RLock()
		defer r.lock.RUnlock()
		if r.handler == nil {
			return vsock.ErrUnhandledMessageType
		}

		resp, err := r.handler(ctx, req)
		if err != nil {
			return err
		}
		out, err := responseCodec.ToMessageWithID(streamMsg.ID, resp)
		if err != nil {
			return err
		}
		return messenger.Send(ctx, out)
	}); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *R[Req, Resp]) Request(ctx context.Context, payload Req) (Resp, error) {
	var zero Resp
	if r == nil || r.messenger == nil {
		return zero, vsock.ErrNilTransport
	}
	if r.requestCodec == nil || r.responseCodec == nil {
		return zero, vsock.ErrNilCodec
	}

	msg, err := r.requestCodec.ToMessage(payload)
	if err != nil {
		return zero, err
	}
	respMsg, err := r.messenger.Request(ctx, msg, r.responseCodec.TypeID())
	if err != nil {
		return zero, err
	}
	return r.responseCodec.Decode(respMsg)
}

func (r *R[Req, Resp]) OnRequest(handler func(context.Context, Req) (Resp, error)) {
	if r == nil {
		return
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	r.handler = handler
}
