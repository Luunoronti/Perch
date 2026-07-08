// Package proto implements the simple binary framing protocol used between
// the perch client and server. See remote-pwsh-terminal-spec.md §4.
package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

type FrameType byte

const (
	FrameAuth    FrameType = 0x01 // reserved, unused (see spec §8)
	FrameResize  FrameType = 0x02
	FrameData    FrameType = 0x03
	FrameAuthOK  FrameType = 0x04 // reserved, unused
	FrameAuthErr FrameType = 0x05 // reserved, unused
	FrameExit    FrameType = 0x06
	FramePing    FrameType = 0x07
	FramePong    FrameType = 0x08
)

// MaxFrameLength is the hard cap on a single frame's payload size.
const MaxFrameLength = 1 << 20 // 1 MiB

// Frame is a single protocol message.
type Frame struct {
	Type    FrameType
	Payload []byte
}

// WriteFrame writes a single frame to w: 1 byte type + 4 byte BE length + payload.
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Payload) > MaxFrameLength {
		return fmt.Errorf("proto: payload too large: %d bytes", len(f.Payload))
	}
	header := make([]byte, 5)
	header[0] = byte(f.Type)
	binary.BigEndian.PutUint32(header[1:], uint32(len(f.Payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads a single frame from r, enforcing MaxFrameLength.
func ReadFrame(r io.Reader) (Frame, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxFrameLength {
		return Frame{}, fmt.Errorf("proto: frame length %d exceeds max %d", length, MaxFrameLength)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Type: FrameType(header[0]), Payload: payload}, nil
}

// EncodeResize packs cols/rows into a RESIZE payload.
func EncodeResize(cols, rows uint16) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint16(buf[0:2], cols)
	binary.BigEndian.PutUint16(buf[2:4], rows)
	return buf
}

// DecodeResize unpacks a RESIZE payload into cols/rows.
func DecodeResize(payload []byte) (cols, rows uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, fmt.Errorf("proto: invalid RESIZE payload length %d", len(payload))
	}
	cols = binary.BigEndian.Uint16(payload[0:2])
	rows = binary.BigEndian.Uint16(payload[2:4])
	return cols, rows, nil
}

// EncodeExit packs an exit code into an EXIT payload.
func EncodeExit(code uint32) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, code)
	return buf
}

// DecodeExit unpacks an EXIT payload into an exit code.
func DecodeExit(payload []byte) (uint32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("proto: invalid EXIT payload length %d", len(payload))
	}
	return binary.BigEndian.Uint32(payload), nil
}
