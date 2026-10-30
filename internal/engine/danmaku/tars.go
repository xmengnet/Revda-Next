package danmaku

// Minimal Tars (JCE) codec for the huya danmaku websocket, mirroring the
// semantics of dmlive's tars_stream crate (dmlive/tars-stream). Field layout:
// one head byte (tag in the high nibble, type in the low nibble; tag >= 15
// moves the tag into a second byte), then the value.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	tarsTypeInt8        = 0
	tarsTypeInt16       = 1
	tarsTypeInt32       = 2
	tarsTypeInt64       = 3
	tarsTypeFloat       = 4
	tarsTypeDouble      = 5
	tarsTypeString1     = 6
	tarsTypeString4     = 7
	tarsTypeMap         = 8
	tarsTypeList        = 9
	tarsTypeStructBegin = 10
	tarsTypeStructEnd   = 11
	tarsTypeZero        = 12
	tarsTypeSimpleList  = 13
)

var errTarsTagNotFound = errors.New("tars: tag not found")

type tarsDecoder struct {
	buf []byte
	pos int
}

func newTarsDecoder(b []byte) *tarsDecoder {
	return &tarsDecoder{buf: b}
}

func (d *tarsDecoder) remaining() int {
	return len(d.buf) - d.pos
}

func (d *tarsDecoder) take(n int) ([]byte, error) {
	if n < 0 || d.remaining() < n {
		return nil, fmt.Errorf("tars: need %d bytes, have %d", n, d.remaining())
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

// takeHead reads one field head. The returned position sits at the value.
func (d *tarsDecoder) takeHead() (tag, typ int, err error) {
	hb, err := d.take(1)
	if err != nil {
		return 0, 0, err
	}
	tag = int(hb[0] >> 4)
	typ = int(hb[0] & 0x0f)
	if tag == 15 {
		tb, err := d.take(1)
		if err != nil {
			return 0, 0, err
		}
		tag = int(tb[0])
	}
	return tag, typ, nil
}

// skipField advances past one field body of the given type.
func (d *tarsDecoder) skipField(typ int) error {
	switch typ {
	case tarsTypeInt8:
		_, err := d.take(1)
		return err
	case tarsTypeInt16:
		_, err := d.take(2)
		return err
	case tarsTypeInt32, tarsTypeFloat:
		_, err := d.take(4)
		return err
	case tarsTypeInt64, tarsTypeDouble:
		_, err := d.take(8)
		return err
	case tarsTypeString1:
		b, err := d.take(1)
		if err != nil {
			return err
		}
		_, err = d.take(int(b[0]))
		return err
	case tarsTypeString4:
		b, err := d.take(4)
		if err != nil {
			return err
		}
		_, err = d.take(int(binary.BigEndian.Uint32(b)))
		return err
	case tarsTypeMap:
		n, err := d.readIntCount()
		if err != nil {
			return err
		}
		for i := 0; i < 2*n; i++ {
			_, typ, err := d.takeHead()
			if err != nil {
				return err
			}
			if err := d.skipField(typ); err != nil {
				return err
			}
		}
		return nil
	case tarsTypeList:
		n, err := d.readIntCount()
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			_, typ, err := d.takeHead()
			if err != nil {
				return err
			}
			if err := d.skipField(typ); err != nil {
				return err
			}
		}
		return nil
	case tarsTypeStructBegin:
		for {
			_, typ, err := d.takeHead()
			if err != nil {
				return err
			}
			if typ == tarsTypeStructEnd {
				return nil
			}
			if err := d.skipField(typ); err != nil {
				return err
			}
		}
	case tarsTypeStructEnd, tarsTypeZero:
		return nil
	case tarsTypeSimpleList:
		// huya dialect: an orphan head byte precedes the length field
		if _, typ, err := d.takeHead(); err != nil {
			return err
		} else if typ != tarsTypeInt8 && typ != tarsTypeInt16 && typ != tarsTypeInt32 {
			return errors.New("tars: simple list size field is not an int")
		}
		n, err := d.readIntCount()
		if err != nil {
			return err
		}
		_, err = d.take(n)
		return err
	default:
		return fmt.Errorf("tars: unknown type %d", typ)
	}
}

// readIntCount reads the element count that follows a map/list/simple-list
// head: a minimal int field at tag 0.
func (d *tarsDecoder) readIntCount() (int, error) {
	n, err := d.readInt(0)
	if err != nil {
		return 0, err
	}
	if n < 0 || n > int64(d.remaining()) {
		return 0, fmt.Errorf("tars: container size %d out of range", n)
	}
	return int(n), nil
}

// skipToTag consumes fields until it reaches tag with a type other than
// STRUCT_END. When absent it restores the position and returns
// errTarsTagNotFound, so failed optional reads don't disturb later ones.
func (d *tarsDecoder) skipToTag(tag int) (int, error) {
	before := d.pos
	for d.remaining() > 0 {
		hTag, hTyp, err := d.takeHead()
		if err != nil {
			d.pos = before
			return 0, err
		}
		if hTag == tag && hTyp != tarsTypeStructEnd {
			return hTyp, nil
		}
		if err := d.skipField(hTyp); err != nil {
			d.pos = before
			return 0, err
		}
	}
	d.pos = before
	return 0, errTarsTagNotFound
}

func (d *tarsDecoder) readIntBody(typ int) (int64, error) {
	switch typ {
	case tarsTypeZero:
		return 0, nil
	case tarsTypeInt8:
		b, err := d.take(1)
		if err != nil {
			return 0, err
		}
		return int64(int8(b[0])), nil
	case tarsTypeInt16:
		b, err := d.take(2)
		if err != nil {
			return 0, err
		}
		return int64(int16(binary.BigEndian.Uint16(b))), nil
	case tarsTypeInt32:
		b, err := d.take(4)
		if err != nil {
			return 0, err
		}
		return int64(int32(binary.BigEndian.Uint32(b))), nil
	case tarsTypeInt64:
		b, err := d.take(8)
		if err != nil {
			return 0, err
		}
		return int64(binary.BigEndian.Uint64(b)), nil
	}
	return 0, fmt.Errorf("tars: mismatched type %d for int", typ)
}

// readInt reads the field at tag as an integer (any int wire width accepted).
func (d *tarsDecoder) readInt(tag int) (int64, error) {
	typ, err := d.skipToTag(tag)
	if err != nil {
		return 0, err
	}
	return d.readIntBody(typ)
}

// readString reads the field at tag as a string.
func (d *tarsDecoder) readString(tag int) (string, error) {
	typ, err := d.skipToTag(tag)
	if err != nil {
		return "", err
	}
	switch typ {
	case tarsTypeString1:
		b, err := d.take(1)
		if err != nil {
			return "", err
		}
		s, err := d.take(int(b[0]))
		if err != nil {
			return "", err
		}
		return string(s), nil
	case tarsTypeString4:
		b, err := d.take(4)
		if err != nil {
			return "", err
		}
		s, err := d.take(int(binary.BigEndian.Uint32(b)))
		if err != nil {
			return "", err
		}
		return string(s), nil
	}
	return "", fmt.Errorf("tars: mismatched type %d for string", typ)
}

// readBytes reads the field at tag as a simple list of raw bytes: the huya
// wire format puts an orphan head byte before the length field.
func (d *tarsDecoder) readBytes(tag int) ([]byte, error) {
	typ, err := d.skipToTag(tag)
	if err != nil {
		return nil, err
	}
	if typ != tarsTypeSimpleList {
		return nil, fmt.Errorf("tars: mismatched type %d for bytes", typ)
	}
	if _, typ, err := d.takeHead(); err != nil {
		return nil, err
	} else if typ != tarsTypeInt8 && typ != tarsTypeInt16 && typ != tarsTypeInt32 {
		return nil, errors.New("tars: simple list size field is not an int")
	}
	n, err := d.readInt(0)
	if err != nil {
		return nil, err
	}
	if n < 0 || n > int64(d.remaining()) {
		return nil, fmt.Errorf("tars: simple list size %d out of range", n)
	}
	return d.take(int(n))
}

// readStruct positions the decoder inside the struct field at tag and returns
// the same decoder: struct fields are read off the shared stream like
// tars_stream does, and the closing STRUCT_END head is skipped implicitly by
// the next skipToTag. Callers must read the struct's fields in tag order.
func (d *tarsDecoder) readStruct(tag int) (*tarsDecoder, error) {
	typ, err := d.skipToTag(tag)
	if err != nil {
		return nil, err
	}
	if typ != tarsTypeStructBegin {
		return nil, fmt.Errorf("tars: mismatched type %d for struct", typ)
	}
	return d, nil
}

type tarsEncoder struct {
	buf []byte
}

func (e *tarsEncoder) putHead(tag, typ int) {
	if tag < 15 {
		e.buf = append(e.buf, byte(tag<<4|typ))
	} else {
		e.buf = append(e.buf, byte(0xf0|typ), byte(tag))
	}
}

func (e *tarsEncoder) writeInt8(tag int, v int8) {
	if v == 0 {
		e.putHead(tag, tarsTypeZero)
		return
	}
	e.putHead(tag, tarsTypeInt8)
	e.buf = append(e.buf, byte(v))
}

func (e *tarsEncoder) writeInt16(tag int, v int16) {
	if v >= math.MinInt8 && v <= math.MaxInt8 {
		e.writeInt8(tag, int8(v))
		return
	}
	e.putHead(tag, tarsTypeInt16)
	e.buf = binary.BigEndian.AppendUint16(e.buf, uint16(v))
}

func (e *tarsEncoder) writeInt32(tag int, v int32) {
	if v >= math.MinInt16 && v <= math.MaxInt16 {
		e.writeInt16(tag, int16(v))
		return
	}
	e.putHead(tag, tarsTypeInt32)
	e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(v))
}

func (e *tarsEncoder) writeInt64(tag int, v int64) {
	if v >= math.MinInt32 && v <= math.MaxInt32 {
		e.writeInt32(tag, int32(v))
		return
	}
	e.putHead(tag, tarsTypeInt64)
	e.buf = binary.BigEndian.AppendUint64(e.buf, uint64(v))
}

func (e *tarsEncoder) writeBool(tag int, v bool) {
	if v {
		e.writeInt8(tag, 1)
	} else {
		e.writeInt8(tag, 0)
	}
}

func (e *tarsEncoder) writeString(tag int, s string) {
	if len(s) <= 255 {
		e.putHead(tag, tarsTypeString1)
		e.buf = append(e.buf, byte(len(s)))
		e.buf = append(e.buf, s...)
		return
	}
	e.putHead(tag, tarsTypeString4)
	e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *tarsEncoder) writeList(tag int, items []string) {
	e.putHead(tag, tarsTypeList)
	e.writeInt32(0, int32(len(items)))
	for _, s := range items {
		e.writeString(0, s)
	}
}

// writeBytes writes a simple list of raw bytes, replicating tars_stream's
// quirk of emitting an orphan head(0, INT8) byte before the length field —
// this is the format the huya gateway expects.
func (e *tarsEncoder) writeBytes(tag int, b []byte) {
	e.putHead(tag, tarsTypeSimpleList)
	e.putHead(0, tarsTypeInt8)
	e.writeInt32(0, int32(len(b)))
	e.buf = append(e.buf, b...)
}
