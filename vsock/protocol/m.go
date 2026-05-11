package protocol

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/hlfshell/poprocks/vsock"
)

type M[T any] struct {
	lock            sync.RWMutex
	codec           *vsock.Codec[T]
	receivers       []func(T)
	streamReceivers []func(context.Context, *vsock.Message) error
	messenger       *vsock.Messenger
}

func NewM[T any](messenger *vsock.Messenger, codec *vsock.Codec[T]) (*M[T], error) {
	if messenger == nil {
		return nil, vsock.ErrNilTransport
	}
	if codec == nil {
		return nil, vsock.ErrNilCodec
	}

	m := &M[T]{
		codec:           codec,
		receivers:       make([]func(T), 0),
		streamReceivers: make([]func(context.Context, *vsock.Message) error, 0),
		messenger:       messenger,
	}

	if err := messenger.OnReceive(codec.TypeID(), func(ctx context.Context, streamMsg *vsock.Message) error {
		m.lock.RLock()
		streamReceivers := append([]func(context.Context, *vsock.Message) error(nil), m.streamReceivers...)
		typedReceivers := append([]func(T){}, m.receivers...)
		m.lock.RUnlock()

		if len(streamReceivers) > 0 {
			for _, receiver := range streamReceivers {
				if err := receiver(ctx, streamMsg); err != nil {
					return err
				}
			}
			return nil
		}

		payload, err := streamMsg.ReadAll()
		if err != nil {
			return err
		}

		var t T
		if codec.Stream {
			t, err = decodeStreamPayload[T](payload)
		} else {
			t, err = codec.Decode(vsock.NewMessage(streamMsg.ID, streamMsg.Type, payload))
		}
		if err != nil {
			return err
		}

		for _, receiver := range typedReceivers {
			receiver(t)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return m, nil
}

func (m *M[T]) Send(ctx context.Context, payload T) error {
	if m == nil || m.codec == nil {
		return vsock.ErrNilCodec
	}
	if m.codec.Stream {
		reader, payloadLen, closer, err := streamSourceFromPayload(any(payload))
		if err != nil {
			return err
		}
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
		_, err = m.messenger.SendStream(ctx, m.codec.TypeID(), payloadLen, reader)
		return err
	}

	msg, err := m.codec.ToMessage(payload)
	if err != nil {
		return err
	}
	return m.messenger.Send(ctx, msg)
}

func (m *M[T]) OnReceive(handler func(T)) {
	if m == nil {
		return
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	m.receivers = append(m.receivers, handler)
}

func (m *M[T]) OnReceiveStream(handler func(context.Context, *vsock.Message) error) error {
	if m == nil {
		return vsock.ErrNilHandler
	}
	if handler == nil {
		return vsock.ErrNilHandler
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	m.streamReceivers = append(m.streamReceivers, handler)
	return nil
}

func decodeStreamPayload[T any](payload []byte) (T, error) {
	var zero T
	switch any(zero).(type) {
	case []byte:
		b := append([]byte(nil), payload...)
		return any(b).(T), nil
	case string:
		return any(string(payload)).(T), nil
	case io.Reader:
		return any(io.Reader(bytes.NewReader(payload))).(T), nil
	default:
		return zero, fmt.Errorf("stream codec does not support in-memory decode for %T; use OnReceiveStream", zero)
	}
}

func streamSourceFromPayload(payload any) (io.Reader, uint32, io.Closer, error) {
	if payload == nil {
		return nil, 0, nil, fmt.Errorf("payload is required")
	}
	if src, ok := payload.(vsock.StreamSource); ok {
		r, l, err := src.StreamSource()
		if err != nil {
			return nil, 0, nil, err
		}
		if r == nil {
			return nil, 0, nil, fmt.Errorf("reader is required")
		}
		return r, l, nil, nil
	}
	return nil, 0, nil, fmt.Errorf("stream payload must implement StreamSource; got %T", payload)
}
