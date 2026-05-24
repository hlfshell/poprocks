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

type Codec[T any] struct {
	codec  CodecType
	typeID uint32

	// Stream indicates if the codec utilizes a stream-based transport
	// specifically; essentially announcing that it is unwise to hold the
	// payload in memory or to wait for its termination.
	Stream bool
}

type CodecType uint8

// Not conclusive, but common patterns and payload types supported.
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
	if !isValidCodecType(codecType) {
		return nil, fmt.Errorf("%w: unsupported codec: %d", ErrInvalidCodecType, codecType)
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

// Decode converts a wire message into T.
//
// Buffered codecs (ie `.Stream` is false) read the complete payload into memory
// before unmarshalling. Stream codecs (`.Stream` is true) intentionally skip
// ReadAll and adapt the message reader into T, so callers can consume the
// payload incrementally.
func (c *Codec[T]) Decode(msg *Message) (T, error) {
	var t T
	if msg == nil {
		return t, errors.New("message is nil")
	}
	if msg.Type != c.typeID {
		return t, fmt.Errorf("message type mismatch: got=%d want=%d", msg.Type, c.typeID)
	}
	if c.Stream {
		// Do not materialize stream payloads. The returned T owns the live
		// reader, and the receiver must consume it before the messenger drains
		// the frame.
		return decodeStreamPayload[T](msg)
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
	id, err := c.generateID()
	if err != nil {
		return nil, err
	}
	return c.ToMessageWithID(id, value)
}

func (c *Codec[T]) ToMessageWithID(id uint64, value T) (*Message, error) {
	payload, err := c.Encode(value)
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

func decodeStreamPayload[T any](msg *Message) (T, error) {
	var t T
	reader := msg.Reader()
	if reader == nil {
		return t, ErrNilMessage
	}

	// Fast paths for the stream payload shapes this package explicitly supports.
	// These avoid reflection and make the intended stream contracts obvious.
	switch out := any(&t).(type) {
	case *ReaderPayload:
		*out = ReaderPayload{Reader: reader, Length: msg.Length}
		return t, nil
	case **ReaderPayload:
		*out = &ReaderPayload{Reader: reader, Length: msg.Length}
		return t, nil
	case *io.Reader:
		*out = reader
		return t, nil
	case **Message:
		*out = msg
		return t, nil
	case *Message:
		*out = *msg
		return t, nil
	}

	// Fallback for user-defined stream payload structs. A value or pointer to a
	// struct may opt in by exposing a settable Reader field and, optionally, a
	// settable Length field.
	v := reflect.ValueOf(&t).Elem()
	if v.Kind() == reflect.Ptr {
		// For pointer T values, allocate the target struct so its fields can be
		// populated before returning the pointer.
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return t, fmt.Errorf("stream codec cannot decode into %T", t)
	}

	// Reader is required: without it, the caller has no way to consume the stream.
	readerField := v.FieldByName("Reader")
	if !readerField.IsValid() || !readerField.CanSet() || !reflect.TypeOf(reader).AssignableTo(readerField.Type()) {
		return t, fmt.Errorf("stream codec cannot set Reader on %T", t)
	}
	readerField.Set(reflect.ValueOf(reader))

	// Length is optional metadata. If present, it must use one of the accepted
	// integer field types so the frame length can be copied without ambiguity.
	lengthField := v.FieldByName("Length")
	if lengthField.IsValid() && lengthField.CanSet() {
		switch lengthField.Kind() {
		case reflect.Uint32:
			lengthField.SetUint(uint64(msg.Length))
		case reflect.Uint64, reflect.Uint, reflect.Uintptr:
			lengthField.SetUint(uint64(msg.Length))
		case reflect.Int64, reflect.Int:
			lengthField.SetInt(int64(msg.Length))
		default:
			return t, fmt.Errorf("stream codec cannot set Length on %T", t)
		}
	}
	return t, nil
}

func isValidCodecType(codecType CodecType) bool {
	switch codecType {
	case CodecMsgpack, CodecGob, CodecJSON, CodecYAML, CodecStream:
		return true
	default:
		return false
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
