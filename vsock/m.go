package vsock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
)

type M[T any] struct {
	lock             sync.RWMutex
	codec            *Codec[T]
	receivers        []func(T) error
	streamReceivers  []func(context.Context, *StreamMessage) error
	streamRegistered bool
	messenger        *Messenger
}

func NewM[T any](messenger *Messenger, codec *Codec[T]) (*M[T], error) {
	if messenger == nil {
		return nil, errors.New("messenger is required")
	}
	originalCodecNil := codec == nil
	if originalCodecNil {
		// Create a codec from the object T
		// Generate typeID from hash of T
		var zero T
		t := reflect.TypeOf(zero)
		if t == nil {
			return nil, errors.New("unable to infer typeID from T; create an explicit codec with NewCodec(...)")
		}
		typeName := t.String()
		// FNV-1a 32-bit offset basis/prime for deterministic, low-cost type
		// hashing.
		var typeID uint32 = 2166136261
		for i := 0; i < len(typeName); i++ {
			typeID ^= uint32(typeName[i])
			typeID *= 16777619
		}
		if typeID == 0 {
			return nil, errors.New("inferred typeID is zero; create an explicit codec with NewCodec(...)")
		}

		// Create a new codec with the typeID
		var err error
		codec, err = NewCodec[T](typeID)
		if err != nil {
			return nil, err
		}
	}

	// Check to see if we have this typeID already registered or not
	messenger.lock.Lock()
	defer messenger.lock.Unlock()

	if _, exists := messenger.receivers[codec.typeID]; exists {
		if originalCodecNil {
			return nil, fmt.Errorf("typeID already registered; create an explicit codec with NewCodec(...)")
		}
		return nil, errors.New("typeID already registered; use the existing codec")
	}

	m := &M[T]{
		lock:             sync.RWMutex{},
		codec:            codec,
		receivers:        make([]func(T) error, 0),
		streamReceivers:  make([]func(context.Context, *StreamMessage) error, 0),
		streamRegistered: false,
		messenger:        messenger,
	}

	messenger.receivers[codec.typeID] = func(ctx context.Context, msg *Message) error {
		var (
			t   T
			err error
		)
		if m.codec.Stream {
			t, err = decodeStreamPayload[T](msg.Payload)
		} else {
			// Convert msg to T
			t, err = m.codec.Decode(msg)
		}
		if err != nil {
			return err
		}

		m.lock.RLock()
		defer m.lock.RUnlock()

		for _, receiver := range m.receivers {
			go func() { receiver(t) }()
		}

		return nil
	}

	return m, nil
}

func (m *M[T]) Send(ctx context.Context, payload T) error {
	if m == nil || m.codec == nil {
		return errors.New("codec is required")
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

func (m *M[T]) OnReceive(handler func(T) error) {
	if m == nil {
		return
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	m.receivers = append(m.receivers, handler)
}

func (m *M[T]) OnReceiveStream(handler func(context.Context, *StreamMessage) error) error {
	if m == nil {
		return ErrNilHandler
	}
	if handler == nil {
		return ErrNilHandler
	}

	m.lock.Lock()
	m.streamReceivers = append(m.streamReceivers, handler)
	shouldRegister := !m.streamRegistered
	m.lock.Unlock()

	if !shouldRegister {
		return nil
	}

	if err := m.messenger.OnStream(m.codec.typeID, func(ctx context.Context, msg *StreamMessage) error {
		m.lock.RLock()
		handlers := append([]func(context.Context, *StreamMessage) error(nil), m.streamReceivers...)
		m.lock.RUnlock()
		for _, receiver := range handlers {
			if err := receiver(ctx, msg); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		m.lock.Lock()
		if len(m.streamReceivers) > 0 {
			m.streamReceivers = m.streamReceivers[:len(m.streamReceivers)-1]
		}
		m.lock.Unlock()
		return err
	}

	m.lock.Lock()
	m.streamRegistered = true
	m.lock.Unlock()
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
