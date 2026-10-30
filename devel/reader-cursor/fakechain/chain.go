// Package fakechain is a deterministic fake blockchain: block n's id and contents are a
// function of n, so a reader can produce any block again, and a checker can verify every
// block it finds in a store.
package fakechain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// BlockType is the protobuf type the reader announces: the payload is a BytesValue holding the
// block's JSON.
const BlockType = "google.protobuf.BytesValue"

// Genesis is the state before the first block.
var Genesis = sha256.Sum256([]byte("fakechain genesis"))

func ID(num uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], num)
	sum := sha256.Sum256(append([]byte("fakechain block "), buf[:]...))
	return hex.EncodeToString(sum[:])
}

// NextState is the stateful chain's state after block num: it depends on every block before,
// so a reader can only compute it by going through them all.
func NextState(prev [32]byte, num uint64) [32]byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], num)
	return sha256.Sum256(append(prev[:], buf[:]...))
}

func Timestamp(num uint64) time.Time {
	return time.Unix(1_700_000_000, 0).Add(time.Duration(num) * time.Second)
}

// Content is a block's payload.
type Content struct {
	Num uint64 `json:"num"`
	// State is the stateful chain's state after this block, hex encoded
	State  string `json:"state,omitempty"`
	Filler string `json:"filler,omitempty"`
}

// Payload encodes content as a google.protobuf.BytesValue.
func Payload(content Content) []byte {
	data, _ := json.Marshal(content)
	out := []byte{0x0a}
	out = binary.AppendUvarint(out, uint64(len(data)))
	return append(out, data...)
}

// DecodePayload decodes a google.protobuf.BytesValue made by Payload.
func DecodePayload(payload []byte) (Content, error) {
	var content Content
	if len(payload) < 2 || payload[0] != 0x0a {
		return content, fmt.Errorf("not a BytesValue")
	}
	size, n := binary.Uvarint(payload[1:])
	if n <= 0 || uint64(len(payload)-1-n) != size {
		return content, fmt.Errorf("bad BytesValue length")
	}
	err := json.Unmarshal(payload[1+n:], &content)
	return content, err
}
