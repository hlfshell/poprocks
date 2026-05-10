package vsock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

type M[T any] struct {
	lock            sync.RWMutex
	codec           *Codec[T]
	receivers       []func(T)
	streamReceivers []func(context.Context, *Message) error
	messenger       *Messenger
}

func NewM[T any](messenger *Messenger, codec *Codec[T]) (*M[T], error) {
	if messenger == nil {
		return nil, ErrNilTransport
	}
	if codec == nil {
		return nil, ErrNilCodec
	}

	// Check to see if we have this typeID already registered or not
	messenger.lock.Lock()
	defer messenger.lock.Unlock()

	if _, exists := messenger.receivers[codec.typeID]; exists {
		return nil, errors.New("typeID already registered; use the existing codec")
	}

	m := &M[T]{
		lock:            sync.RWMutex{},
		codec:           codec,
		receivers:       make([]func(T), 0),
		streamReceivers: make([]func(context.Context, *Message) error, 0),
		messenger:       messenger,
	}

	messenger.receivers[codec.typeID] = func(ctx context.Context, streamMsg *Message) error {
		m.lock.RLock()
		streamReceivers := append([]func(context.Context, *Message) error(nil), m.streamReceivers...)
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

		msg, err := materializeStreamMessage(streamMsg)
		if err != nil {
			return err
		}
		payload, err := msg.ReadAll()
		if err != nil {
			return err
		}

		var (
			t T
		)
		if m.codec.Stream {
			t, err = decodeStreamPayload[T](payload)
		} else {
			// Convert msg to T
			t, err = m.codec.Decode(msg)
		}
		if err != nil {
			return err
		}

		for _, receiver := range typedReceivers {
			receiver(t)
		}

		return nil
	}

	return m, nil
}

func (m *M[T]) Send(ctx context.Context, payload T) error {
	if m == nil || m.codec == nil {
		return ErrNilCodec
	}
	if m.codec.Stream {
		reader, payloadLen, closer, err := streamSourceFromPayload(any(payload))
		if err != nil {
			return err
		}
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
		id, err := m.codec.generateID()
		if err != nil {
			return err
		}
		return m.messenger.SendStreamWithID(ctx, id, m.codec.typeID, payloadLen, reader)
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

func (m *M[T]) OnReceiveStream(handler func(context.Context, *Message) error) error {
	if m == nil {
		return ErrNilHandler
	}
	if handler == nil {
		return ErrNilHandler
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
