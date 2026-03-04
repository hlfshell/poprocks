package vsock

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// StreamMessage exposes a message payload as a forward-only stream.
// Callers can either materialize payload bytes in memory or stream directly
// into an io.Writer (for example, an on-disk file).
type StreamMessage struct {
	Message *Message
	reader  *io.LimitedReader
}

type StreamSource interface {
	StreamSource() (io.Reader, uint32, error)
}

type ReaderPayload struct {
	Reader io.Reader
	Length uint32
}

func (p ReaderPayload) StreamSource() (io.Reader, uint32, error) {
	if p.Reader == nil {
		return nil, 0, fmt.Errorf("reader is required")
	}
	return p.Reader, p.Length, nil
}

func newStreamMessage(msg *Message, r io.Reader) *StreamMessage {
	return &StreamMessage{
		Message: msg,
		reader: &io.LimitedReader{
			R: r,
			N: int64(msg.Length),
		},
	}
}

func (s *StreamMessage) Header() *Message {
	return s.Message
}

func (s *StreamMessage) Reader() io.Reader {
	if s == nil || s.reader == nil {
		return nil
	}
	return s.reader
}

func (s *StreamMessage) ReadAll() ([]byte, error) {
	if s == nil || s.reader == nil {
		return nil, ErrNilMessage
	}
	return io.ReadAll(s.reader)
}

func (s *StreamMessage) WriteTo(w io.Writer) (int64, error) {
	if s == nil || s.reader == nil {
		return 0, ErrNilMessage
	}
	if w == nil {
		return 0, fmt.Errorf("writer is required")
	}
	return io.Copy(w, s.reader)
}

func (s *StreamMessage) drain() error {
	if s == nil || s.reader == nil || s.reader.N == 0 {
		return nil
	}
	_, err := io.Copy(io.Discard, s.reader)
	return err
}

func (m *Messenger) newMessageID() (uint64, error) {
	for {
		var idBytes [8]byte
		if _, err := rand.Read(idBytes[:]); err != nil {
			return 0, err
		}
		id := binary.BigEndian.Uint64(idBytes[:])
		if id != 0 {
			return id, nil
		}
	}
}

// SendStream sends a framed message where the payload is read from r without
// buffering the full payload in memory.
func (m *Messenger) SendStream(ctx context.Context, msgType uint32, payloadLen uint32, r io.Reader) (uint64, error) {
	id, err := m.newMessageID()
	if err != nil {
		return 0, err
	}
	if err := m.SendStreamWithID(ctx, id, msgType, payloadLen, r); err != nil {
		return 0, err
	}
	return id, nil
}

// SendStreamWithID sends a framed message with the given ID and payload stream.
func (m *Messenger) SendStreamWithID(ctx context.Context, id uint64, msgType uint32, payloadLen uint32, r io.Reader) error {
	if m == nil || m.vsock == nil {
		return ErrNilTransport
	}
	if r == nil {
		return fmt.Errorf("reader is required")
	}
	if id == 0 {
		return fmt.Errorf("id is required")
	}

	totalLen := headerLength + int(payloadLen)
	if m.config.MaxMessageSize > 0 && totalLen > m.config.MaxMessageSize {
		return fmt.Errorf("message size too large")
	}

	m.writeLock.Lock()
	defer m.writeLock.Unlock()

	header := make([]byte, headerLength)
	binary.BigEndian.PutUint64(header[0:8], id)
	binary.BigEndian.PutUint32(header[8:12], msgType)
	binary.BigEndian.PutUint32(header[12:16], payloadLen)

	if err := m.writeAll(ctx, header); err != nil {
		return err
	}

	remaining := int64(payloadLen)
	buf := make([]byte, 32*1024)
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		chunk := int64(len(buf))
		if chunk > remaining {
			chunk = remaining
		}
		n, err := r.Read(buf[:chunk])
		if n > 0 {
			if err := m.writeAll(ctx, buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && remaining == 0 {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return nil
}

func (m *Messenger) writeAll(ctx context.Context, payload []byte) error {
	for len(payload) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := m.vsock.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func (m *Messenger) readHeader() (*Message, error) {
	if m == nil || m.vsock == nil {
		return nil, ErrNilTransport
	}

	header := make([]byte, headerLength)
	if _, err := io.ReadFull(m.vsock, header); err != nil {
		return nil, err
	}

	msg := &Message{
		ID:     binary.BigEndian.Uint64(header[0:8]),
		Type:   binary.BigEndian.Uint32(header[8:12]),
		Length: binary.BigEndian.Uint32(header[12:16]),
	}
	if msg.ID == 0 {
		return nil, fmt.Errorf("id is required")
	}
	if m.config.MaxMessageSizeReceived > 0 && int(msg.Length) > m.config.MaxMessageSizeReceived {
		return nil, fmt.Errorf("received message size too large")
	}
	return msg, nil
}

func materializeStreamMessage(streamMsg *StreamMessage) (*Message, error) {
	if streamMsg == nil || streamMsg.Message == nil {
		return nil, ErrNilMessage
	}
	msg := &Message{
		ID:     streamMsg.Message.ID,
		Type:   streamMsg.Message.Type,
		Length: streamMsg.Message.Length,
	}
	if msg.Length == 0 {
		msg.Payload = nil
		return msg, nil
	}

	payload, err := streamMsg.ReadAll()
	if err != nil {
		return nil, err
	}
	msg.Payload = payload
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	return msg, nil
}

func streamSourceFromPayload(payload any) (io.Reader, uint32, io.Closer, error) {
	if payload == nil {
		return nil, 0, nil, fmt.Errorf("payload is required")
	}
	if src, ok := payload.(StreamSource); ok {
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
