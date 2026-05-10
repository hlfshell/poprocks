package vsock

import (
	"context"
	"errors"
	"sync"
)

type R[Req any, Resp any] struct {
	lock          sync.RWMutex
	requestCodec  *Codec[Req]
	responseCodec *Codec[Resp]
	messenger     *Messenger
	handler       func(context.Context, Req) (Resp, error)
}

func NewR[Req any, Resp any](messenger *Messenger, requestCodec *Codec[Req], responseCodec *Codec[Resp]) (*R[Req, Resp], error) {
	if messenger == nil {
		return nil, ErrNilTransport
	}
	if requestCodec == nil || responseCodec == nil {
		return nil, ErrNilCodec
	}

	messenger.lock.Lock()
	defer messenger.lock.Unlock()

	if _, exists := messenger.receivers[requestCodec.typeID]; exists {
		return nil, errors.New("typeID already registered; use the existing codec")
	}

	r := &R[Req, Resp]{
		lock:          sync.RWMutex{},
		requestCodec:  requestCodec,
		responseCodec: responseCodec,
		messenger:     messenger,
	}

	messenger.receivers[requestCodec.typeID] = func(ctx context.Context, streamMsg *Message) error {
		msg, err := materializeStreamMessage(streamMsg)
		if err != nil {
			return err
		}

		req, err := requestCodec.Decode(msg)
		if err != nil {
			return err
		}

		r.lock.RLock()
		handler := r.handler
		r.lock.RUnlock()
		if handler == nil {
			return ErrUnhandledMessageType
		}

		resp, err := handler(ctx, req)
		if err != nil {
			return err
		}
		out, err := responseCodec.ToMessageWithID(msg.ID, resp)
		if err != nil {
			return err
		}
		return messenger.Send(ctx, out)
	}

	return r, nil
}

func (r *R[Req, Resp]) Request(ctx context.Context, payload Req) (Resp, error) {
	var zero Resp
	if r == nil || r.messenger == nil {
		return zero, ErrNilTransport
	}
	if r.requestCodec == nil || r.responseCodec == nil {
		return zero, ErrNilCodec
	}

	id, err := r.requestCodec.generateID()
	if err != nil {
		return zero, err
	}
	msg, err := r.requestCodec.ToMessageWithID(id, payload)
	if err != nil {
		return zero, err
	}

	wait := r.messenger.registerPendingResponse(id, r.responseCodec.TypeID())
	defer r.messenger.unregisterPendingResponse(id, wait)

	if err := r.messenger.Send(ctx, msg); err != nil {
		return zero, err
	}

	select {
	case respMsg, ok := <-wait:
		if !ok || respMsg == nil {
			return zero, ErrNilMessage
		}
		return r.responseCodec.Decode(respMsg)
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (r *R[Req, Resp]) OnRequest(handler func(context.Context, Req) (Resp, error)) {
	if r == nil {
		return
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	r.handler = handler
}
