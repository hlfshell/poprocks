package protocol

import (
	"context"
	"sync"

	"github.com/hlfshell/poprocks/vsock"
)

/*
R[Req, Resp] is a request/response wrapper for vsock messaging. It allows you to
define request and response types so that more complex messaging patterns can be
built, such as more useful function calling.

The request and response codecs each decide whether their payloads use buffered
or stream transport.
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
		// Snapshot the registered handler before decoding or invoking user
		// code. This matches M: the lock protects registration, not handler
		// execution.
		r.lock.RLock()
		handler := r.handler
		r.lock.RUnlock()
		if handler == nil {
			return nil
		}

		// Decode gets the original stream-backed message. Buffered request
		// codecs will materialize and unmarshal it; stream request codecs will
		// pass a live reader through Req so the handler decides how to consume
		// it.
		req, err := requestCodec.Decode(streamMsg)
		if err != nil {
			return err
		}

		resp, err := handler(ctx, req)
		if err != nil {
			return err
		}
		if responseCodec.Stream {
			// Stream responses must preserve the request ID so Messenger can
			// correlate this frame with the pending Request call.
			reader, payloadLen, closer, err := vsock.StreamSourceFromPayload(any(resp))
			if err != nil {
				return err
			}
			if closer != nil {
				defer func() { _ = closer.Close() }()
			}
			return messenger.SendStreamWithID(ctx, streamMsg.ID, responseCodec.TypeID(), payloadLen, reader)
		}
		// Buffered responses are encoded into a normal message with the same
		// request ID for response correlation.
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

	var respMsg *vsock.Message
	if r.requestCodec.Stream {
		// Stream requests use the stream transport path while still registering a
		// pending response for the generated request ID.
		reader, payloadLen, closer, err := vsock.StreamSourceFromPayload(any(payload))
		if err != nil {
			return zero, err
		}
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
		respMsg, err = r.messenger.RequestStream(ctx, r.requestCodec.TypeID(), payloadLen, reader, r.responseCodec.TypeID())
		if err != nil {
			return zero, err
		}
	} else {
		// Buffered requests are encoded into memory before send.
		msg, err := r.requestCodec.ToMessage(payload)
		if err != nil {
			return zero, err
		}
		respMsg, err = r.messenger.Request(ctx, msg, r.responseCodec.TypeID())
		if err != nil {
			return zero, err
		}
	}
	// Decode gets the response message exactly like M's receive path. Buffered
	// response codecs materialize; stream response codecs pass through a reader.
	return r.responseCodec.Decode(respMsg)
}

func (r *R[Req, Resp]) OnRequest(handler func(context.Context, Req) (Resp, error)) error {
	if r == nil {
		return vsock.ErrNilHandler
	}
	if handler == nil {
		return vsock.ErrNilHandler
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.handler != nil {
		return vsock.ErrHandlerAlreadyRegistered
	}
	r.handler = handler
	return nil
}

// RemoveReceiver removes the registered request receiver.
func (r *R[Req, Resp]) RemoveReceiver() bool {
	if r == nil {
		return false
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.handler == nil {
		return false
	}
	r.handler = nil
	return true
}
