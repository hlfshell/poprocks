package vsock

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
	"gopkg.in/yaml.v2"
)

// Message is an envelope payload for a given message. It is the raw
// message being sent over the wire. It works by sending:
// 1. An 8-byte UUID (uint64)
// 2. A 4-byte type (uint32)
// 3. A 4-byte big-endian length prefix (uint32)
//
// The ID is for ACK, error handling, timing, etc Type can be ignored (null'ed)
// if not needed. Per application assignment of the types.
type Message struct {
	Header
	Payload io.Reader `json:"-"`

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
	msg.Payload = limited
	return msg
}

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

type Codec[T any] struct {
	codec  CodecType
	typeID uint32

	// Stream indicates if the codec utilizes a stream-based transport
	// specifically; essentially announcing that it is unwise to hold the
	// payload in memory or to wait for its termination.
	Stream bool
}

type CodecType uint8

const (
	CodecUnknown CodecType = iota
	CodecMsgpack
	CodecGob
	CodecJSON
	CodecYAML
	CodecStream
)

func NewCodec[T any](typeID uint32) (*Codec[T], error) {
	if typeID == 0 {
		return nil, fmt.Errorf("%w: must be non-zero", ErrInvalidTypeID)
	}
	codec := inferCodecType[T]()
	if codec == CodecUnknown {
		return nil, fmt.Errorf("%w: add msgpack/json/yaml tags on T or use NewCodecWithEncoding", ErrInvalidCodecType)
	}

	return &Codec[T]{
		typeID: typeID,
		codec:  codec,
		Stream: codec == CodecStream,
	}, nil
}

func NewCodecOfType[T any](typeID uint32, codecType CodecType) (*Codec[T], error) {
	if typeID == 0 {
		return nil, fmt.Errorf("%w: must be non-zero", ErrInvalidTypeID)
	}

	return &Codec[T]{
		typeID: typeID,
		codec:  codecType,
		Stream: codecType == CodecStream,
	}, nil
}

func (c *Codec[T]) generateID() (uint64, error) {
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

func (c *Codec[T]) Encode(value T) ([]byte, error) {
	return encodeWith(c.codec, value)
}

func (c *Codec[T]) Decode(msg *Message) (T, error) {
	var t T
	if msg == nil {
		return t, errors.New("message is nil")
	}
	if msg.Type != c.typeID {
		return t, fmt.Errorf("message type mismatch: got=%d want=%d", msg.Type, c.typeID)
	}
	payload, err := msg.ReadAll()
	if err != nil {
		return t, err
	}

	if err := decodeWith(c.codec, payload, &t); err != nil {
		return t, err
	}
	return t, nil
}

func (c *Codec[T]) ToMessage(value T) (*Message, error) {
	payload, err := c.Encode(value)
	if err != nil {
		return nil, err
	}
	id, err := c.generateID()
	if err != nil {
		return nil, err
	}
	return NewMessage(id, c.typeID, payload), nil
}

func (c *Codec[T]) TypeID() uint32 {
	return c.typeID
}

func encodeWith[T any](codec CodecType, value T) ([]byte, error) {
	switch codec {
	case CodecMsgpack:
		return msgpack.Marshal(value)
	case CodecGob:
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(value); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case CodecJSON:
		return json.Marshal(value)
	case CodecYAML:
		return yaml.Marshal(value)
	case CodecStream:
		return nil, fmt.Errorf("stream codec requires stream transport")
	default:
		return nil, fmt.Errorf("unsupported codec: %d", codec)
	}
}

func decodeWith[T any](codec CodecType, payload []byte, out *T) error {
	switch codec {
	case CodecMsgpack:
		return msgpack.Unmarshal(payload, out)
	case CodecGob:
		return gob.NewDecoder(bytes.NewReader(payload)).Decode(out)
	case CodecJSON:
		return json.Unmarshal(payload, out)
	case CodecYAML:
		return yaml.Unmarshal(payload, out)
	case CodecStream:
		return fmt.Errorf("stream codec does not support byte-buffer decode")
	default:
		return fmt.Errorf("unsupported codec: %d", codec)
	}
}

func inferCodecType[T any]() CodecType {
	t := reflect.TypeFor[T]()
	if t == nil {
		return CodecUnknown
	}
	if isStreamType(t) {
		return CodecStream
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if hasStructTag(t, "msgpack", map[reflect.Type]bool{}) {
		return CodecMsgpack
	}
	if hasStructTag(t, "json", map[reflect.Type]bool{}) {
		return CodecJSON
	}
	if hasStructTag(t, "yaml", map[reflect.Type]bool{}) {
		return CodecYAML
	}
	return CodecUnknown
}
