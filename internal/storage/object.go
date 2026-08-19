package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/MarkAndrewKamau/shardstore/internal/ec"
)

// DefaultStripeSize is the stripe size used when none is configured: 8 MiB.
const DefaultStripeSize = 8 << 20

// ObjectStore implements single-node object read/write over a ShardStore:
// an object is split into stripes, each stripe is erasure-coded into k+m
// shards, and reads reconstruct from surviving shards, treating checksum
// failures as lost shards.
//
// Reads are streaming (one stripe in memory at a time). Concurrent writes to
// the same object ID are not safe; serialization arrives with the metadata
// service in Phase 3.
type ObjectStore struct {
	store      ShardStore
	params     ec.Params
	stripeSize int64
}

// NewObjectStore builds an ObjectStore over store.
func NewObjectStore(store ShardStore, params ec.Params, stripeSize int64) *ObjectStore {
	if stripeSize <= 0 {
		stripeSize = DefaultStripeSize
	}
	return &ObjectStore{store: store, params: params, stripeSize: stripeSize}
}

// Params returns the EC scheme in use.
func (o *ObjectStore) Params() ec.Params { return o.params }

// PutObject reads size bytes from r, encodes them stripe by stripe, and
// persists all shards. A zero-size object stores only its marker.
func (o *ObjectStore) PutObject(ctx context.Context, objectID string, r io.Reader, size int64) error {
	if objectID == "" {
		return fmt.Errorf("storage: empty object id")
	}
	if size < 0 {
		return fmt.Errorf("storage: negative size %d", size)
	}
	enc, err := ec.EncoderFor(o.params)
	if err != nil {
		return err
	}

	buf := make([]byte, o.stripeSize)
	var stripeIdx uint32
	remaining := size
	for remaining > 0 {
		chunk := buf
		if remaining < o.stripeSize {
			chunk = buf[:remaining]
		}
		if _, err := io.ReadFull(r, chunk); err != nil {
			return fmt.Errorf("storage: read input: %w", err)
		}
		if err := o.putStripe(ctx, enc, objectID, stripeIdx, chunk); err != nil {
			return err
		}
		stripeIdx++
		remaining -= int64(len(chunk))
	}

	return o.store.WriteMarker(ctx, objectID, ObjectMeta{
		Size:    size,
		Stripes: stripeIdx,
		Params:  o.params,
	})
}

func (o *ObjectStore) putStripe(ctx context.Context, enc *ec.Encoder, objectID string, stripeIdx uint32, stripe []byte) error {
	shards, err := enc.Encode(stripe)
	if err != nil {
		return err
	}
	for i, s := range shards {
		h := ShardHeader{
			ObjectID:     objectID,
			StripeIdx:    stripeIdx,
			ShardIdx:     uint32(i),
			DataShards:   uint16(o.params.DataShards),
			ParityShards: uint16(o.params.ParityShards),
			StripeLen:    int64(len(stripe)),
			DataLen:      int64(len(s)),
		}
		if err := o.store.PutShard(ctx, ShardKey{ObjectID: objectID, StripeIdx: stripeIdx, ShardIdx: uint32(i)}, h, s); err != nil {
			return fmt.Errorf("storage: put shard %d of stripe %d: %w", i, stripeIdx, err)
		}
	}
	return nil
}

// GetObject streams an object back, reconstructing on the fly from surviving
// shards. It errors with ErrUnrecoverable when fewer than k shards of any
// stripe are valid, and ErrObjectNotFound when the object has no shards and
// no marker.
func (o *ObjectStore) GetObject(ctx context.Context, objectID string) (io.ReadCloser, int64, error) {
	keys, err := o.store.ListObjectShards(ctx, objectID)
	if err != nil {
		return nil, 0, err
	}
	if len(keys) == 0 {
		meta, err := o.store.ReadMarker(ctx, objectID)
		if err != nil {
			if errors.Is(err, ErrObjectNotFound) {
				return nil, 0, ErrObjectNotFound
			}
			return nil, 0, err
		}
		return io.NopCloser(emptyReader{}), meta.Size, nil
	}

	var maxStripe uint32
	for _, k := range keys {
		if k.StripeIdx > maxStripe {
			maxStripe = k.StripeIdx
		}
	}
	stripes := maxStripe + 1

	// Object size = full stripes + the last stripe's original length.
	size := int64(stripes-1) * o.stripeSize
	lastLen, err := o.lastStripeLen(ctx, objectID, maxStripe)
	if err != nil {
		return nil, 0, err
	}
	size += lastLen

	return &objectReader{
		ctx:          ctx,
		store:        o.store,
		enc:          o.encoder(),
		objectID:     objectID,
		params:       o.params,
		stripeSize:   o.stripeSize,
		totalStripes: stripes,
	}, size, nil
}

func (o *ObjectStore) encoder() *ec.Encoder {
	enc, err := ec.EncoderFor(o.params)
	if err != nil {
		panic(err) // params are validated at construction
	}
	return enc
}

// lastStripeLen returns the original (unpadded) length of the last stripe,
// reading the first valid shard's header only.
func (o *ObjectStore) lastStripeLen(ctx context.Context, objectID string, stripeIdx uint32) (int64, error) {
	for shardIdx := 0; shardIdx < o.params.ShardCount(); shardIdx++ {
		h, err := o.store.VerifyShard(ctx, ShardKey{ObjectID: objectID, StripeIdx: stripeIdx, ShardIdx: uint32(shardIdx)})
		if err != nil {
			if errors.Is(err, ErrCorruptShard) || errors.Is(err, ErrShardNotFound) {
				continue
			}
			return 0, err
		}
		return h.StripeLen, nil
	}
	return 0, fmt.Errorf("storage: %w: no valid shards for last stripe %d", ErrUnrecoverable, stripeIdx)
}

// ObjectShards returns the shard keys stored for objectID, or
// ErrObjectNotFound when the object has no shards and no marker.
func (o *ObjectStore) ObjectShards(ctx context.Context, objectID string) ([]ShardKey, error) {
	keys, err := o.store.ListObjectShards(ctx, objectID)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		if _, err := o.store.ReadMarker(ctx, objectID); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// DeleteObject removes all shards and the marker of objectID.
func (o *ObjectStore) DeleteObject(ctx context.Context, objectID string) error {
	keys, err := o.store.ListObjectShards(ctx, objectID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := o.store.DeleteShard(ctx, k); err != nil {
			return err
		}
	}
	return o.store.RemoveMarker(ctx, objectID)
}

// objectReader streams an object stripe by stripe, reconstructing each from
// the valid shards available.
type objectReader struct {
	ctx          context.Context
	store        ShardStore
	enc          *ec.Encoder
	objectID     string
	params       ec.Params
	stripeSize   int64
	totalStripes uint32

	stripeIdx uint32
	buf       []byte
	off       int
}

func (r *objectReader) Read(p []byte) (int, error) {
	if r.stripeIdx >= r.totalStripes {
		return 0, io.EOF
	}
	if r.off == len(r.buf) {
		if err := r.loadStripe(); err != nil {
			return 0, err
		}
		if len(r.buf) == 0 {
			r.stripeIdx++
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	if r.off == len(r.buf) {
		r.stripeIdx++
		r.buf = nil
		r.off = 0
	}
	return n, nil
}

// loadStripe reads every shard of the current stripe, drops invalid ones
// (missing or checksum-failed), and reconstructs from the survivors.
func (r *objectReader) loadStripe() error {
	total := r.params.ShardCount()
	shards := make([][]byte, total)
	var stripeLen int64

	valid, lost := 0, 0
	for i := 0; i < total; i++ {
		data, h, err := r.store.GetShard(r.ctx, ShardKey{ObjectID: r.objectID, StripeIdx: r.stripeIdx, ShardIdx: uint32(i)})
		switch {
		case err == nil:
			shards[i] = data
			stripeLen = h.StripeLen
			valid++
		case errors.Is(err, ErrCorruptShard), errors.Is(err, ErrShardNotFound):
			lost++
		default:
			return err
		}
	}
	if valid < r.params.DataShards {
		return fmt.Errorf("storage: stripe %d: %w: %d of %d shards valid", r.stripeIdx, ErrUnrecoverable, valid, total)
	}
	if lost > 0 {
		if err := r.enc.Reconstruct(shards); err != nil {
			return fmt.Errorf("storage: stripe %d: %w", r.stripeIdx, err)
		}
	}

	out := make([]byte, stripeLen)
	if err := r.enc.Join(out, shards, int(stripeLen)); err != nil {
		return fmt.Errorf("storage: stripe %d: %w", r.stripeIdx, err)
	}
	r.buf = out
	r.off = 0
	return nil
}

func (r *objectReader) Close() error { return nil }

type emptyReader struct{}

func (emptyReader) Read(p []byte) (int, error) { return 0, io.EOF }

// markerFileName is the per-object metadata file next to its shard
// directory. It distinguishes an empty object (no shards) from a nonexistent
// one until the Phase 3 metadata service takes over.
const markerFileName = ".meta"
