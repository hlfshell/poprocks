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
	lock            sync.RWMutex
	codec           *Codec[T]
	receivers       []func(T) error
	streamReceivers []func(context.Context, *Message) error
	messenger       *Messenger
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
		lock:            sync.RWMutex{},
		codec:           codec,
		receivers:       make([]func(T) error, 0),
		streamReceivers: make([]func(context.Context, *Message) error, 0),
		messenger:       messenger,
	}

	messenger.receivers[codec.typeID] = func(ctx context.Context, streamMsg *Message) error {
		m.lock.RLock()
		streamReceivers := append([]func(context.Context, *Message) error(nil), m.streamReceivers...)
		typedReceivers := append([]func(T) error(nil), m.receivers...)
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
