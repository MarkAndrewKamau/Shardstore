// Package ec implements Reed-Solomon erasure coding for shardstore.
//
// A stripe of object data is split into k data shards and encoded with m
// parity shards. Any k of the k+m shards suffice to reconstruct the stripe;
// up to m shards can be lost or corrupted without data loss.
//
// Encoding and reconstruction are stripe-granular so memory stays bounded by
// the stripe size regardless of object size. The underlying engine is
// github.com/klauspost/reedsolomon.
package ec

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/klauspost/reedsolomon"
)

// Params describes an erasure coding scheme.
type Params struct {
	DataShards   int
	ParityShards int
}

// DefaultParams returns the default scheme: 4 data + 2 parity (1.5x overhead,
// tolerates 2 failures).
func DefaultParams() Params {
	return Params{DataShards: 4, ParityShards: 2}
}

// String returns a compact label like "4+2".
func (p Params) String() string {
	return fmt.Sprintf("%d+%d", p.DataShards, p.ParityShards)
}

// ShardCount returns the total number of shards per stripe.
func (p Params) ShardCount() int { return p.DataShards + p.ParityShards }

// MaxLoss reports how many shard failures the scheme tolerates.
func (p Params) MaxLoss() int { return p.ParityShards }

// Validate checks that p describes a usable scheme.
func (p Params) Validate() error {
	switch {
	case p.DataShards < 1:
		return fmt.Errorf("ec: data shards must be >= 1, got %d", p.DataShards)
	case p.ParityShards < 1:
		return fmt.Errorf("ec: parity shards must be >= 1, got %d", p.ParityShards)
	case p.ShardCount() > 256:
		return fmt.Errorf("ec: total shards %d exceeds the 256-shard limit of Reed-Solomon", p.ShardCount())
	}
	return nil
}

// Encoder wraps a reedsolomon.Encoder for a fixed Params, with an encoder
// cache keyed by (k, m).
type Encoder struct {
	enc  reedsolomon.Encoder
	parm Params
}

var encCache sync.Map // Params -> *Encoder

// EncoderFor returns a cached or new Encoder for p.
func EncoderFor(p Params) (*Encoder, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if v, ok := encCache.Load(p); ok {
		return v.(*Encoder), nil
	}
	enc, err := reedsolomon.New(p.DataShards, p.ParityShards)
	if err != nil {
		return nil, fmt.Errorf("ec: create encoder: %w", err)
	}
	e := &Encoder{enc: enc, parm: p}
	actual, _ := encCache.LoadOrStore(p, e)
	return actual.(*Encoder), nil
}

// SplitSize returns the per-shard payload length for a stripe of length n:
// ceil(n / k), the padded size reedsolomon uses.
func (e *Encoder) SplitSize(n int) int {
	if n == 0 {
		return 0
	}
	return (n + e.parm.DataShards - 1) / e.parm.DataShards
}

// Encode splits stripe into k data shards plus m parity shards.
// Each returned shard is len-ceil(len(stripe)/k); the final data shards are
// zero-padded by the RS split. The caller must not mutate the input after
// calling Encode; the data shards share backing memory with stripe.
func (e *Encoder) Encode(stripe []byte) ([][]byte, error) {
	if len(stripe) == 0 {
		// The RS library refuses to Split empty input; an empty stripe
		// encodes to k+m empty shards (all parity is trivially zero).
		shards := make([][]byte, e.parm.ShardCount())
		for i := range shards {
			shards[i] = []byte{}
		}
		return shards, nil
	}
	shards, err := e.enc.Split(stripe)
	if err != nil {
		return nil, fmt.Errorf("ec: split: %w", err)
	}
	if err := e.enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("ec: encode: %w", err)
	}
	return shards, nil
}

// Reconstruct fills in missing shards in place. Shards with len 0 or nil are
// treated as lost. At least k shards must be present and equal length.
// On success every shard is populated and can be joined.
func (e *Encoder) Reconstruct(shards [][]byte) error {
	if allEmpty(shards) {
		// Empty stripes have nothing to reconstruct; the library refuses
		// all-empty input, so short-circuit: empty shards stay empty.
		return nil
	}
	if err := e.enc.Reconstruct(shards); err != nil {
		return fmt.Errorf("ec: reconstruct: %w", err)
	}
	return nil
}

func allEmpty(shards [][]byte) bool {
	for _, s := range shards {
		if len(s) != 0 {
			return false
		}
	}
	return true
}

// ReconstructSome reconstructs only the shards listed in present (all others
// assumed lost). It is a convenience wrapper around Reconstruct.
func (e *Encoder) ReconstructSome(shards [][]byte, present []bool) error {
	for i := range shards {
		if !present[i] {
			shards[i] = nil
		}
	}
	return e.Reconstruct(shards)
}

// Verify reports whether the shards form a consistent codeword. It returns
// true only if every parity shard matches its data shards.
func (e *Encoder) Verify(shards [][]byte) (bool, error) {
	ok, err := e.enc.Verify(shards)
	if err != nil {
		return false, fmt.Errorf("ec: verify: %w", err)
	}
	return ok, nil
}

// Join writes the reconstructed stripe (exactly size bytes) into dst from
// shards; shards must all be present (e.g., after Reconstruct). dst must be
// pre-sized to size; its contents are overwritten.
func (e *Encoder) Join(dst []byte, shards [][]byte, size int) error {
	if err := e.enc.Join(bytes.NewBuffer(dst[:0]), shards, size); err != nil {
		return fmt.Errorf("ec: join: %w", err)
	}
	return nil
}

// ShardPayloadLen is the padded per-shard payload length for a stripe of
// length n under params p.
func ShardPayloadLen(n int, p Params) int {
	e, err := EncoderFor(p)
	if err != nil {
		return 0
	}
	return e.SplitSize(n)
}
