package protocol

import (
	"context"
	"sync"

	"github.com/hlfshell/poprocks/vsock"
)

/*
M[T] is a simple send and forget message type. It can be treated as a sender,
receiver, or both.

It allows you to define a type, a Codec, and a receiver. The Codec determines
how to convert the incoming payload back to the given type.

Only one receiver may be registered at a time. There is no expectation of
success or failure - that is to be implemented by the receiver and resulting
core logic.

Buffered codecs convert the entire payload into memory and then pass it to the
registered receiver. Stream codecs pass a stream-backed payload.
*/
type M[T any] struct {
	lock      sync.RWMutex
	codec     *vsock.Codec[T]
	receiver  func(context.Context, T) error
	messenger *vsock.Messenger
}

func NewM[T any](messenger *vsock.Messenger, codec *vsock.Codec[T]) (*M[T], error) {
	if messenger == nil {
		return nil, vsock.ErrNilTransport
	}
	if codec == nil {
		return nil, vsock.ErrNilCodec
	}

	m := &M[T]{
		codec:     codec,
		messenger: messenger,
	}

	if err := messenger.OnReceive(codec.TypeID(), func(ctx context.Context, streamMsg *vsock.Message) error {
		m.lock.RLock()
		receiver := m.receiver
		m.lock.RUnlock()
		if receiver == nil {
			return nil
		}

		// Decode gets the original stream-backed message. Buffered codecs will
		// materialize and unmarshal it; stream codecs will pass a live reader
		// through T so the receiver decides how to consume the payload.
		t, err := codec.Decode(streamMsg)
		if err != nil {
			return err
		}
		return receiver(ctx, t)
	}); err != nil {
		return nil, err
	}

	return m, nil
}

func (m *M[T]) Send(ctx context.Context, payload T) error {
	if m == nil || m.codec == nil {
		return vsock.ErrNilCodec
	}

	// If the codec is a stream codec, we need to convert the payload to a
	// stream source.
	if m.codec.Stream {
		reader, payloadLen, closer, err := vsock.StreamSourceFromPayload(any(payload))
		if err != nil {
			return err
		}
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
		_, err = m.messenger.SendStream(ctx, m.codec.TypeID(), payloadLen, reader)
		return err
	}

	// ...otherwise we just convert the payload to a message and send it.
	msg, err := m.codec.ToMessage(payload)
	if err != nil {
		return err
	}
	return m.messenger.Send(ctx, msg)
}

func (m *M[T]) OnReceive(handler func(context.Context, T) error) error {
	if m == nil {
		return vsock.ErrNilHandler
	}
	if handler == nil {
		return vsock.ErrNilHandler
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.receiver != nil {
		return vsock.ErrHandlerAlreadyRegistered
	}
	m.receiver = handler
	return nil
}

// RemoveReceiver removes the currently registered receiver.
func (m *M[T]) RemoveReceiver() bool {
	if m == nil {
		return false
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.receiver == nil {
		return false
	}
	m.receiver = nil
	return true
}
