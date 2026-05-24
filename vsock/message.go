package vsock

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Message is an envelope payload for a given message. It is the raw message
// being sent over the wire. It works by sending:
// 1. An 8-byte UUID (uint64)
// 2. A 4-byte type (uint32)
// 3. A 4-byte big-endian length prefix (uint32)
//
// The ID is for ACK, error handling, timing, etc.Type can be ignored (null'ed)
// if not needed. Per application assignment of the types.
type Message struct {
	Header

	payloadReader *io.LimitedReader
	payloadCache  []byte
}

type Header struct {
	ID     uint64 `json:"id"`
	Type   uint32 `json:"type"`
	Length uint32 `json:"length"`
}

const headerLength = 16 // 8-byte ID + 4-byte Type + 4-byte Length

func (m *Message) Validate() error {
	if m.ID == 0 {
		return errors.New("id is required")
	}
	if m.payloadCache != nil && len(m.payloadCache) != int(m.Length) {
		return errors.New("payload length mismatch")
	}
	return nil
}

func (m *Message) Binary() []byte {
	payload, err := m.ReadAll()
	if err != nil {
		return nil
	}
	payloadLen := uint32(len(payload))
	raw := make([]byte, headerLength+len(payload))

	binary.BigEndian.PutUint64(raw[0:8], m.ID)
	binary.BigEndian.PutUint32(raw[8:12], m.Type)
	binary.BigEndian.PutUint32(raw[12:16], payloadLen)
	copy(raw[headerLength:], payload)

	return raw
}

func NewMessage(id uint64, msgType uint32, payload []byte) *Message {
	copied := append([]byte(nil), payload...)
	msg := &Message{
		Header: Header{
			ID:     id,
			Type:   msgType,
			Length: uint32(len(copied)),
		},
		payloadCache: copied,
	}
	reader := bytes.NewReader(copied)
	limited := &io.LimitedReader{R: reader, N: int64(len(copied))}
	msg.payloadReader = limited
	return msg
}

func newMessageFromHeader(msg *Message, r io.Reader) *Message {
	if msg == nil {
		return nil
	}
	limited := &io.LimitedReader{
		R: r,
		N: int64(msg.Length),
	}
	msg.payloadReader = limited
	return msg
}

func (m *Message) Reader() io.Reader {
	if m == nil || m.payloadReader == nil {
		return nil
	}
	return m.payloadReader
}

func (m *Message) ReadAll() ([]byte, error) {
	if m == nil {
		return nil, ErrNilMessage
	}
	if m.payloadCache != nil {
		out := append([]byte(nil), m.payloadCache...)
		return out, nil
	}
	if m.payloadReader == nil {
		if m.Length == 0 {
			return nil, nil
		}
		return nil, ErrNilMessage
	}
	payload, err := io.ReadAll(m.payloadReader)
	if err != nil {
		return nil, err
	}
	m.payloadCache = payload
	cachedReader := bytes.NewReader(m.payloadCache)
	limited := &io.LimitedReader{R: cachedReader, N: int64(len(m.payloadCache))}
	m.payloadReader = limited
	return append([]byte(nil), payload...), nil
}

func (m *Message) WriteTo(w io.Writer) (int64, error) {
	if m == nil {
		return 0, ErrNilMessage
	}
	if w == nil {
		return 0, errors.New("writer is required")
	}
	if m.payloadCache != nil {
		r := bytes.NewReader(m.payloadCache)
		return io.Copy(w, r)
	}
	if m.payloadReader == nil {
		if m.Length == 0 {
			return 0, nil
		}
		return 0, ErrNilMessage
	}
	return io.Copy(w, m.payloadReader)
}

func (m *Message) drain() error {
	if m == nil || m.payloadReader == nil || m.payloadReader.N == 0 {
		return nil
	}
	_, err := io.Copy(io.Discard, m.payloadReader)
	return err
}

// ParseBinary accepts a raw byte and separates the payload and headers
func ParseBinary(raw []byte) (*Message, error) {
	if len(raw) < headerLength {
		return nil, errors.New("raw data too short for header")
	}

	id := binary.BigEndian.Uint64(raw[0:8])
	msgType := binary.BigEndian.Uint32(raw[8:12])
	length := binary.BigEndian.Uint32(raw[12:16])

	payloadLen := len(raw) - headerLength
	if payloadLen != int(length) {
		return nil, errors.New("payload length mismatch")
	}

	payload := make([]byte, payloadLen)
	copy(payload, raw[headerLength:])

	msg := NewMessage(id, msgType, payload)
	msg.Length = length
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	return msg, nil
}
