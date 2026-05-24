package vsock

import (
	"context"
	"fmt"
	"time"
)

const acknowledgeTypeID uint32 = 0xFFFFFFFF

func (m *Messenger) shouldAwaitAcknowledge(msgType uint32) bool {
	return m.config.RequireAcknowledge && msgType != acknowledgeTypeID
}

func (m *Messenger) registerPendingAck(id uint64) chan struct{} {
	ch := make(chan struct{})
	m.ackLock.Lock()
	m.pendingAcks[id] = ch
	m.ackLock.Unlock()
	return ch
}

func (m *Messenger) unregisterPendingAck(id uint64, ch chan struct{}) {
	m.ackLock.Lock()
	if current, ok := m.pendingAcks[id]; ok && current == ch {
		delete(m.pendingAcks, id)
	}
	m.ackLock.Unlock()
}

func (m *Messenger) resolveAck(id uint64) {
	m.ackLock.Lock()
	ch, ok := m.pendingAcks[id]
	if ok {
		delete(m.pendingAcks, id)
	}
	m.ackLock.Unlock()
	if ok {
		close(ch)
	}
}

func (m *Messenger) waitForAcknowledge(ctx context.Context, id uint64, ch chan struct{}) error {
	if ch == nil {
		return fmt.Errorf("ack channel is required")
	}
	timeout := m.config.Timeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer m.unregisterPendingAck(id, ch)

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("acknowledgement timeout for message %d: %w", id, context.DeadlineExceeded)
	}
}

func (m *Messenger) sendAcknowledge(ctx context.Context, msg *Message) error {
	if m == nil || msg == nil || msg.Type == acknowledgeTypeID {
		return nil
	}
	ack := NewMessage(msg.ID, acknowledgeTypeID, nil)
	go func() {
		_ = m.Send(ctx, ack)
	}()
	return nil
}
