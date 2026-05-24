package vsock

import (
	"context"
	"fmt"
	"io"
)

func (m *Messenger) Request(ctx context.Context, msg *Message, responseType uint32) (*Message, error) {
	if m == nil || m.vsock == nil {
		return nil, ErrNilTransport
	}
	if msg == nil {
		return nil, ErrNilMessage
	}
	if responseType == 0 {
		return nil, ErrInvalidTypeID
	}

	return m.request(ctx, msg.ID, responseType, func() error {
		return m.Send(ctx, msg)
	})
}

func (m *Messenger) RequestStream(ctx context.Context, msgType uint32, payloadLen uint32, r io.Reader, responseType uint32) (*Message, error) {
	if m == nil || m.vsock == nil {
		return nil, ErrNilTransport
	}
	if r == nil {
		return nil, fmt.Errorf("reader is required")
	}
	if msgType == 0 || responseType == 0 {
		return nil, ErrInvalidTypeID
	}
	id, err := m.newMessageID()
	if err != nil {
		return nil, err
	}
	return m.request(ctx, id, responseType, func() error {
		return m.SendStreamWithID(ctx, id, msgType, payloadLen, r)
	})
}

func (m *Messenger) request(ctx context.Context, id uint64, responseType uint32, send func() error) (*Message, error) {
	wait, err := m.registerPendingResponse(id, responseType)
	if err != nil {
		return nil, err
	}
	defer m.unregisterPendingResponse(id, wait)

	if err := send(); err != nil {
		return nil, err
	}

	select {
	case respMsg, ok := <-wait:
		if !ok || respMsg == nil {
			return nil, ErrNilMessage
		}
		return respMsg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type pendingResponse struct {
	msgType uint32
	ch      chan *Message
}

func (m *Messenger) registerPendingResponse(id uint64, msgType uint32) (chan *Message, error) {
	ch := make(chan *Message, 1)
	m.respLock.Lock()
	defer m.respLock.Unlock()
	if _, exists := m.pendingResponses[id]; exists {
		return nil, ErrDuplicateMessageID
	}
	m.pendingResponses[id] = pendingResponse{msgType: msgType, ch: ch}
	return ch, nil
}

func (m *Messenger) unregisterPendingResponse(id uint64, ch chan *Message) {
	m.respLock.Lock()
	if current, ok := m.pendingResponses[id]; ok && current.ch == ch {
		delete(m.pendingResponses, id)
	}
	m.respLock.Unlock()
}

func (m *Messenger) resolveResponse(msg *Message) (bool, error) {
	if m == nil || msg == nil {
		return false, nil
	}
	m.respLock.Lock()
	pending, ok := m.pendingResponses[msg.ID]
	if ok && pending.msgType == msg.Type {
		delete(m.pendingResponses, msg.ID)
	}
	m.respLock.Unlock()
	if !ok || pending.msgType != msg.Type {
		return false, nil
	}
	pr, pw := io.Pipe()
	out := &Message{
		Header: msg.Header,
	}
	newMessageFromHeader(out, pr)
	pending.ch <- out
	close(pending.ch)

	_, err := msg.WriteTo(pw)
	if closeErr := pw.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = pr.CloseWithError(err)
		return false, err
	}
	return true, nil
}
