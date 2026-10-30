package danmaku

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
)

func zlibInflate(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func binaryPutUint16(b []byte, v uint16) {
	binary.BigEndian.PutUint16(b, v)
}

func binaryPutUint32(b []byte, v uint32) {
	binary.BigEndian.PutUint32(b, v)
}

func binaryUint16(b []byte) uint16 {
	return binary.BigEndian.Uint16(b)
}

func binaryUint32(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}
