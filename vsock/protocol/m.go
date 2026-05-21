package protocol

import (
	"context"
	"fmt"
	"sync"

	"github.com/hlfshell/poprocks/vsock"
)

/*
M[T] is a simple send and forget message type. It can be treated as a sender,
receiver, or both.

It allows you to define a type, a Codec, and, and either receivers or stream
receivers. The Codec determines how to convert the incoming raw bytes back to
the given type.

All receivers are invoked in parallel. There is no expectation of success or
failure - that is to be implemented by the receiver and resuting core logic.

Receivers convert the entire payload into memory and then pass it to the
registered receiver. Stream receivers receive the raw bytes and can process them
in chunks - preferred for larger payloads.

All receivers feed back an ID you can utilize to later remove it.
*/
type M[T any] struct {
	lock            sync.RWMutex
	codec           *vsock.Codec[T]
	receivers       map[string]func(T)
	streamReceivers map[string]func(context.Context, *vsock.Message) error
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
		receivers:       make(map[string]func(T)),
		streamReceivers: make(map[string]func(context.Context, *vsock.Message) error),
		messenger:       messenger,
	}

	if err := messenger.OnReceive(codec.TypeID(), func(ctx context.Context, streamMsg *vsock.Message) error {
		m.lock.RLock()
		defer m.lock.RUnlock()

		if len(m.streamReceivers) > 0 {
			var wg sync.WaitGroup
			errs := make(chan error, len(m.streamReceivers))
			for _, receiver := range m.streamReceivers {
				receiver := receiver
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := receiver(ctx, streamMsg); err != nil {
						errs <- err
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				return err
			}
			return nil
		}

		if codec.Stream {
			return fmt.Errorf("stream codec requires OnReceiveStream")
		}

		payload, err := streamMsg.ReadAll()
		if err != nil {
			return err
		}

		t, err := codec.Decode(vsock.NewMessage(streamMsg.ID, streamMsg.Type, payload))
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		for _, receiver := range m.receivers {
			receiver := receiver
			wg.Add(1)
			go func() {
				defer wg.Done()
				receiver(t)
			}()
		}
		wg.Wait()
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

	// If the codec is a stream codec, we need to convert the payload to a stream source.
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

func (m *M[T]) OnReceive(handler func(T)) (string, error) {
	if m == nil {
		return "", vsock.ErrNilHandler
	}
	if handler == nil {
		return "", vsock.ErrNilHandler
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	id := newReceiverID(func(id string) bool {
		if _, exists := m.receivers[id]; exists {
			return true
		}
		_, exists := m.streamReceivers[id]
		return exists
	})

	m.receivers[id] = handler
	return id, nil
}

func (m *M[T]) OnReceiveStream(handler func(context.Context, *vsock.Message) error) (string, error) {
	if m == nil {
		return "", vsock.ErrNilHandler
	}
	if handler == nil {
		return "", vsock.ErrNilHandler
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	id := newReceiverID(func(id string) bool {
		if _, exists := m.receivers[id]; exists {
			return true
		}
		_, exists := m.streamReceivers[id]
		return exists
	})
	m.streamReceivers[id] = handler
	return id, nil
}

// RemoveReceiver removes a receiver by ID. Do not call this from inside a
// receiver callback; schedule it asynchronously to avoid waiting on dispatch.
func (m *M[T]) RemoveReceiver(id string) bool {
	if m == nil || id == "" {
		return false
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if _, ok := m.receivers[id]; !ok {
		return false
	}
	delete(m.receivers, id)
	return true
}

// RemoveStreamReceiver removes a stream receiver by ID. Do not call this from
// inside a stream receiver callback; schedule it asynchronously to avoid waiting
// on dispatch.
func (m *M[T]) RemoveStreamReceiver(id string) bool {
	if m == nil || id == "" {
		return false
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if _, ok := m.streamReceivers[id]; !ok {
		return false
	}
	delete(m.streamReceivers, id)
	return true
}
