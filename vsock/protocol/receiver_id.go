package protocol

import (
	"crypto/rand"
	"encoding/hex"
)

func newReceiverID(exists func(string) bool) string {
	for {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			continue
		}
		id := hex.EncodeToString(b[:])
		if exists != nil && exists(id) {
			continue
		}
		return id
	}
}
