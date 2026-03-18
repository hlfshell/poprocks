package vsock

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type msgpackPayload struct {
	Name  string `msgpack:"name"`
	Count int    `msgpack:"count"`
}

type jsonPayload struct {
	Name string `json:"name"`
}

type yamlPayload struct {
	Name string `yaml:"name"`
}

type unknownPayload struct {
	Name string
}

type recursivePayload struct {
	Child *recursivePayload `json:"child"`
}

type dualTaggedPayload struct {
	Name string `msgpack:"name" json:"name"`
}

func TestMessageValidate(t *testing.T) {
	t.Run("valid message", func(t *testing.T) {
		msg := NewMessage(1, 3, []byte{1, 2})
		if err := msg.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
	})

	t.Run("zero ID", func(t *testing.T) {
		msg := NewMessage(0, 1, nil)
		if err := msg.Validate(); err == nil {
			t.Fatal("Validate() expected error for zero ID")
		}
	})

	t.Run("length mismatch", func(t *testing.T) {
		msg := NewMessage(1, 1, []byte{1, 2})
		msg.Length = 5
		if err := msg.Validate(); err == nil {
			t.Fatal("Validate() expected payload length mismatch error")
		}
	})
}

func TestMessageBinaryAndParseRoundTrip(t *testing.T) {
	original := NewMessage(42, 7, []byte("hello"))
	original.Length = 999 // Binary() should ignore stale Length and compute from payload.

	raw := original.Binary()
	parsed, err := ParseBinary(raw)
	if err != nil {
		t.Fatalf("ParseBinary() unexpected error: %v", err)
	}

	if parsed.ID != original.ID {
		t.Fatalf("ID mismatch: got=%d want=%d", parsed.ID, original.ID)
	}
	if parsed.Type != original.Type {
		t.Fatalf("Type mismatch: got=%d want=%d", parsed.Type, original.Type)
	}
	originalPayload, err := original.ReadAll()
	if err != nil {
		t.Fatalf("read original payload: %v", err)
	}
	if got, want := parsed.Length, uint32(len(originalPayload)); got != want {
		t.Fatalf("Length mismatch: got=%d want=%d", got, want)
	}
	parsedPayload, err := parsed.ReadAll()
	if err != nil {
		t.Fatalf("read parsed payload: %v", err)
	}
	if string(parsedPayload) != string(originalPayload) {
		t.Fatalf("Payload mismatch: got=%q want=%q", parsedPayload, originalPayload)
	}
}

func TestParseBinaryErrors(t *testing.T) {
	t.Run("too short for header", func(t *testing.T) {
		if _, err := ParseBinary([]byte{1, 2, 3}); err == nil {
			t.Fatal("ParseBinary() expected short-header error")
		}
	})

	t.Run("payload length mismatch", func(t *testing.T) {
		raw := make([]byte, headerLength+3)
		binary.BigEndian.PutUint64(raw[0:8], 1)
		binary.BigEndian.PutUint32(raw[8:12], 2)
		binary.BigEndian.PutUint32(raw[12:16], 9) // incorrect length
		copy(raw[headerLength:], []byte{1, 2, 3})

		if _, err := ParseBinary(raw); err == nil {
			t.Fatal("ParseBinary() expected payload length mismatch error")
		}
	})

	t.Run("invalid zero ID", func(t *testing.T) {
		raw := make([]byte, headerLength)
		binary.BigEndian.PutUint64(raw[0:8], 0)
		binary.BigEndian.PutUint32(raw[8:12], 2)
		binary.BigEndian.PutUint32(raw[12:16], 0)

		if _, err := ParseBinary(raw); err == nil {
			t.Fatal("ParseBinary() expected ID validation error")
		}
	})
}

func TestNewCodecErrors(t *testing.T) {
	t.Run("invalid type ID", func(t *testing.T) {
		_, err := NewCodec[msgpackPayload](0)
		if err == nil {
			t.Fatal("NewCodec() expected invalid typeID error")
		}
		if !errors.Is(err, ErrInvalidTypeID) {
			t.Fatalf("expected ErrInvalidTypeID, got: %v", err)
		}
	})

	t.Run("unknown codec type", func(t *testing.T) {
		_, err := NewCodec[unknownPayload](5)
		if err == nil {
			t.Fatal("NewCodec() expected unknown codec type error")
		}
		if !errors.Is(err, ErrInvalidCodecType) {
			t.Fatalf("expected ErrInvalidCodecType, got: %v", err)
		}
	})

	t.Run("invalid explicit encoding", func(t *testing.T) {
		codec, err := NewCodecOfType[msgpackPayload](1, CodecType(255))
		if err != nil {
			t.Fatalf("NewCodecWithEncoding() unexpected error: %v", err)
		}
		if _, err := codec.ToMessage(msgpackPayload{Name: "x"}); err == nil {
			t.Fatal("ToMessage() expected unsupported codec error")
		}
	})
}

func TestCodecRoundTripMsgpack(t *testing.T) {
	codec, err := NewCodec[msgpackPayload](9)
	if err != nil {
		t.Fatalf("NewCodec() unexpected error: %v", err)
	}

	input := msgpackPayload{Name: "poprocks", Count: 3}
	msg, err := codec.ToMessage(input)
	if err != nil {
		t.Fatalf("ToMessage() unexpected error: %v", err)
	}
	if msg.ID == 0 {
		t.Fatal("ToMessage() generated zero ID")
	}
	if got, want := msg.Type, uint32(9); got != want {
		t.Fatalf("Type mismatch: got=%d want=%d", got, want)
	}
	payload, err := msg.ReadAll()
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if got, want := msg.Length, uint32(len(payload)); got != want {
		t.Fatalf("Length mismatch: got=%d want=%d", got, want)
	}

	out, err := codec.Decode(msg)
	if err != nil {
		t.Fatalf("Decode() unexpected error: %v", err)
	}
	if out != input {
		t.Fatalf("decoded payload mismatch: got=%+v want=%+v", out, input)
	}
}

func TestCodecRoundTripJSON(t *testing.T) {
	codec, err := NewCodec[jsonPayload](11)
	if err != nil {
		t.Fatalf("NewCodec() unexpected error: %v", err)
	}

	input := jsonPayload{Name: "json"}
	msg, err := codec.ToMessage(input)
	if err != nil {
		t.Fatalf("ToMessage() unexpected error: %v", err)
	}

	out, err := codec.Decode(msg)
	if err != nil {
		t.Fatalf("Decode() unexpected error: %v", err)
	}
	if out != input {
		t.Fatalf("decoded payload mismatch: got=%+v want=%+v", out, input)
	}
}

func TestCodecRoundTripYAML(t *testing.T) {
	codec, err := NewCodec[yamlPayload](13)
	if err != nil {
		t.Fatalf("NewCodec() unexpected error: %v", err)
	}

	input := yamlPayload{Name: "yaml"}
	msg, err := codec.ToMessage(input)
	if err != nil {
		t.Fatalf("ToMessage() unexpected error: %v", err)
	}

	out, err := codec.Decode(msg)
	if err != nil {
		t.Fatalf("Decode() unexpected error: %v", err)
	}
	if out != input {
		t.Fatalf("decoded payload mismatch: got=%+v want=%+v", out, input)
	}
}

func TestCodecRoundTripGobExplicit(t *testing.T) {
	codec, err := NewCodecOfType[unknownPayload](14, CodecGob)
	if err != nil {
		t.Fatalf("NewCodecWithEncoding() unexpected error: %v", err)
	}

	input := unknownPayload{Name: "gob"}
	msg, err := codec.ToMessage(input)
	if err != nil {
		t.Fatalf("ToMessage() unexpected error: %v", err)
	}

	out, err := codec.Decode(msg)
	if err != nil {
		t.Fatalf("Decode() unexpected error: %v", err)
	}
	if out != input {
		t.Fatalf("decoded payload mismatch: got=%+v want=%+v", out, input)
	}
}

func TestCodecDecodeErrors(t *testing.T) {
	codec, err := NewCodec[msgpackPayload](12)
	if err != nil {
		t.Fatalf("NewCodec() unexpected error: %v", err)
	}

	t.Run("nil message", func(t *testing.T) {
		if _, err := codec.Decode(nil); err == nil {
			t.Fatal("Decode() expected nil message error")
		}
	})

	t.Run("type mismatch", func(t *testing.T) {
		msg := NewMessage(1, 99, nil)
		if _, err := codec.Decode(msg); err == nil {
			t.Fatal("Decode() expected type mismatch error")
		}
	})
}

func TestEncodeDecodeWithUnsupportedCodec(t *testing.T) {
	if _, err := encodeWith(CodecUnknown, jsonPayload{Name: "x"}); err == nil {
		t.Fatal("encodeWith(codecUnknown) expected error")
	}

	var out jsonPayload
	if err := decodeWith(CodecUnknown, []byte("x"), &out); err == nil {
		t.Fatal("decodeWith(codecUnknown) expected error")
	}
}

func TestInferCodecType(t *testing.T) {
	if got := inferCodecType[msgpackPayload](); got != CodecMsgpack {
		t.Fatalf("inferCodecType[msgpackPayload] got=%d want=%d", got, CodecMsgpack)
	}
	if got := inferCodecType[jsonPayload](); got != CodecJSON {
		t.Fatalf("inferCodecType[jsonPayload] got=%d want=%d", got, CodecJSON)
	}
	if got := inferCodecType[unknownPayload](); got != CodecUnknown {
		t.Fatalf("inferCodecType[unknownPayload] got=%d want=%d", got, CodecUnknown)
	}
	if got := inferCodecType[recursivePayload](); got != CodecJSON {
		t.Fatalf("inferCodecType[recursivePayload] got=%d want=%d", got, CodecJSON)
	}
	if got := inferCodecType[yamlPayload](); got != CodecYAML {
		t.Fatalf("inferCodecType[yamlPayload] got=%d want=%d", got, CodecYAML)
	}
	if got := inferCodecType[dualTaggedPayload](); got != CodecMsgpack {
		t.Fatalf("inferCodecType[dualTaggedPayload] got=%d want=%d", got, CodecMsgpack)
	}
	if got := inferCodecType[io.Reader](); got != CodecStream {
		t.Fatalf("inferCodecType[io.Reader] got=%d want=%d", got, CodecStream)
	}
	if got := inferCodecType[ReaderPayload](); got != CodecStream {
		t.Fatalf("inferCodecType[ReaderPayload] got=%d want=%d", got, CodecStream)
	}
	if got := inferCodecType[*ReaderPayload](); got != CodecStream {
		t.Fatalf("inferCodecType[*ReaderPayload] got=%d want=%d", got, CodecStream)
	}
}
