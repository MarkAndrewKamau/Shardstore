// Package storage persists erasure-coded shards and implements single-node
// object read/write over them.
package storage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/MarkAndrewKamau/shardstore/internal/ec"
)

// On-disk magic constants and format versions.
var (
	shardMagic  = [8]byte{'S', 'H', 'A', 'R', 'S', 'H', '0', '1'}
	objectMagic = [8]byte{'S', 'H', 'A', 'R', 'O', 'B', 'J', '1'}
	shardVer    = uint16(1)
	objectVer   = uint16(1)
)

// Sentinel errors returned by the storage layer.
var (
	// ErrShardNotFound reports a shard that is absent from the store.
	ErrShardNotFound = errors.New("shard not found")
	// ErrCorruptShard reports a shard whose payload fails its SHA-256
	// checksum — i.e. silent corruption (bit-rot) or a torn write.
	ErrCorruptShard = errors.New("shard checksum mismatch")
	// ErrObjectNotFound reports an object that has no shards and no marker.
	ErrObjectNotFound = errors.New("object not found")
	// ErrUnrecoverable reports an object that cannot be reconstructed: more
	// than m shards of a stripe are lost or corrupt.
	ErrUnrecoverable = errors.New("object unrecoverable: too many shards lost")
)

// ShardKey identifies one shard of one stripe of one object.
type ShardKey struct {
	ObjectID  string
	StripeIdx uint32
	ShardIdx  uint32
}

// ShardHeader is the self-describing metadata stored at the head of every
// shard file. It is fully redundant with the object index so a shard can be
// verified and repaired without consulting metadata.
type ShardHeader struct {
	ObjectID     string
	StripeIdx    uint32
	ShardIdx     uint32
	DataShards   uint16
	ParityShards uint16
	StripeLen    int64 // original stripe byte length before split/padding
	DataLen      int64 // payload byte length: ceil(StripeLen / DataShards)
	Checksum     [32]byte
}

// Params returns the EC scheme recorded in the header.
func (h ShardHeader) Params() ec.Params {
	return ec.Params{DataShards: int(h.DataShards), ParityShards: int(h.ParityShards)}
}

// EncodeShardHeader serializes h in the little-endian shard file format:
//
//	magic[8] version u16 objLen u16 objectID stripeIdx u32 shardIdx u32
//	k u16 m u16 stripeLen u64 dataLen u64 checksum[32]
func encodeShardHeader(h ShardHeader) ([]byte, error) {
	if h.ObjectID == "" {
		return nil, fmt.Errorf("storage: shard header: empty object id")
	}
	if h.DataShards < 1 || h.ParityShards < 1 {
		return nil, fmt.Errorf("storage: shard header: invalid EC params %d+%d", h.DataShards, h.ParityShards)
	}
	if len(h.ObjectID) > 65535 {
		return nil, fmt.Errorf("storage: shard header: object id too long (%d bytes)", len(h.ObjectID))
	}

	var buf bytes.Buffer
	buf.Write(shardMagic[:])
	_ = binary.Write(&buf, binary.LittleEndian, shardVer)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(h.ObjectID)))
	buf.WriteString(h.ObjectID)
	_ = binary.Write(&buf, binary.LittleEndian, h.StripeIdx)
	_ = binary.Write(&buf, binary.LittleEndian, h.ShardIdx)
	_ = binary.Write(&buf, binary.LittleEndian, h.DataShards)
	_ = binary.Write(&buf, binary.LittleEndian, h.ParityShards)
	_ = binary.Write(&buf, binary.LittleEndian, uint64(h.StripeLen))
	_ = binary.Write(&buf, binary.LittleEndian, uint64(h.DataLen))
	buf.Write(h.Checksum[:])
	return buf.Bytes(), nil
}

// DecodeShardHeader reads and validates a shard header from r.
func decodeShardHeader(r io.Reader) (ShardHeader, error) {
	var h ShardHeader

	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return h, fmt.Errorf("storage: read magic: %w", err)
	}
	if magic != shardMagic {
		return h, fmt.Errorf("storage: bad shard magic %q", magic)
	}
	var ver uint16
	if err := binary.Read(r, binary.LittleEndian, &ver); err != nil {
		return h, fmt.Errorf("storage: read version: %w", err)
	}
	if ver != shardVer {
		return h, fmt.Errorf("storage: unsupported shard version %d", ver)
	}
	var objLen uint16
	if err := binary.Read(r, binary.LittleEndian, &objLen); err != nil {
		return h, fmt.Errorf("storage: read object id length: %w", err)
	}
	objID := make([]byte, objLen)
	if _, err := io.ReadFull(r, objID); err != nil {
		return h, fmt.Errorf("storage: read object id: %w", err)
	}
	h.ObjectID = string(objID)

	fields := []any{
		&h.StripeIdx, &h.ShardIdx, &h.DataShards, &h.ParityShards,
	}
	for _, f := range fields {
		if err := binary.Read(r, binary.LittleEndian, f); err != nil {
			return h, fmt.Errorf("storage: read header field: %w", err)
		}
	}
	var stripeLen, dataLen uint64
	for _, f := range []any{&stripeLen, &dataLen} {
		if err := binary.Read(r, binary.LittleEndian, f); err != nil {
			return h, fmt.Errorf("storage: read length field: %w", err)
		}
	}
	h.StripeLen, h.DataLen = int64(stripeLen), int64(dataLen)

	if _, err := io.ReadFull(r, h.Checksum[:]); err != nil {
		return h, fmt.Errorf("storage: read checksum: %w", err)
	}
	return h, nil
}

// ObjectMeta is the marker written for an object, primarily to distinguish an
// empty object (zero shards) from a nonexistent one. The object index will
// supersede it in Phase 3.
type ObjectMeta struct {
	Size    int64
	Stripes uint32
	Params  ec.Params
}

func encodeObjectMeta(m ObjectMeta) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(objectMagic[:])
	_ = binary.Write(&buf, binary.LittleEndian, objectVer)
	_ = binary.Write(&buf, binary.LittleEndian, uint64(m.Size))
	_ = binary.Write(&buf, binary.LittleEndian, m.Stripes)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(m.Params.DataShards))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(m.Params.ParityShards))
	return buf.Bytes(), nil
}

func decodeObjectMeta(b []byte) (ObjectMeta, error) {
	var m ObjectMeta
	r := bytes.NewReader(b)
	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return m, fmt.Errorf("storage: read object meta magic: %w", err)
	}
	if magic != objectMagic {
		return m, fmt.Errorf("storage: bad object meta magic %q", magic)
	}
	var ver uint16
	if err := binary.Read(r, binary.LittleEndian, &ver); err != nil {
		return m, fmt.Errorf("storage: read object meta version: %w", err)
	}
	if ver != objectVer {
		return m, fmt.Errorf("storage: unsupported object meta version %d", ver)
	}
	var size uint64
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return m, fmt.Errorf("storage: read object size: %w", err)
	}
	m.Size = int64(size)
	if err := binary.Read(r, binary.LittleEndian, &m.Stripes); err != nil {
		return m, fmt.Errorf("storage: read stripe count: %w", err)
	}
	var k, parity uint16
	if err := binary.Read(r, binary.LittleEndian, &k); err != nil {
		return m, fmt.Errorf("storage: read data shards: %w", err)
	}
	if err := binary.Read(r, binary.LittleEndian, &parity); err != nil {
		return m, fmt.Errorf("storage: read parity shards: %w", err)
	}
	m.Params = ec.Params{DataShards: int(k), ParityShards: int(parity)}
	return m, nil
}
