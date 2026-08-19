package ec

import (
	"bytes"
	"fmt"
	"math/bits"
	"math/rand"
	"testing"
)

func randBytes(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

// TestFaultMatrix proves that ANY k of the k+m shards reconstruct the
// original stripe, for every possible surviving subset. For 4+2 that is all
// C(6,4)=15 subsets; for 6+3 all C(9,6)=84 subsets. Surviving subsets that
// are all parity are included implicitly (the mask scan does not restrict
// which shards survive).
func TestFaultMatrix(t *testing.T) {
	cases := []struct {
		name   string
		params Params
		sizes  []int
	}{
		{name: "4+2", params: Params{DataShards: 4, ParityShards: 2}, sizes: []int{1, 100, 4096, 8<<20 + 123}},
		{name: "6+3", params: Params{DataShards: 6, ParityShards: 3}, sizes: []int{1, 100, 4096, 8<<20 + 123}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range tc.sizes {
				t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
					data := randBytes(size, int64(size)+1)
					e, err := EncoderFor(tc.params)
					if err != nil {
						t.Fatal(err)
					}
					shards, err := e.Encode(data)
					if err != nil {
						t.Fatal(err)
					}
					if len(shards) != tc.params.ShardCount() {
						t.Fatalf("shards = %d, want %d", len(shards), tc.params.ShardCount())
					}
					for _, s := range shards {
						if len(s) != e.SplitSize(size) {
							t.Fatalf("shard len = %d, want %d", len(s), e.SplitSize(size))
						}
					}

					total := tc.params.ShardCount()
					for mask := 0; mask < 1<<total; mask++ {
						if bits.OnesCount(uint(mask)) != tc.params.DataShards {
							continue
						}
						work := make([][]byte, total)
						for i := range work {
							if mask&(1<<i) != 0 {
								cp := make([]byte, len(shards[i]))
								copy(cp, shards[i])
								work[i] = cp
							}
						}
						if err := e.Reconstruct(work); err != nil {
							t.Errorf("mask %08b: Reconstruct: %v", mask, err)
							continue
						}
						out := make([]byte, size)
						if err := e.Join(out, work, size); err != nil {
							t.Errorf("mask %08b: Join: %v", mask, err)
							continue
						}
						if !bytes.Equal(out, data) {
							t.Errorf("mask %08b: reconstruction mismatch", mask)
						}
					}
				})
			}
		})
	}
}

// TestReconstructTooFewFails verifies the scheme refuses to reconstruct with
// fewer than k shards.
func TestReconstructTooFewFails(t *testing.T) {
	p := DefaultParams()
	e, _ := EncoderFor(p)
	shards, _ := e.Encode(randBytes(256, 1))
	work := make([][]byte, len(shards))
	for i := 0; i < p.DataShards-1; i++ {
		cp := make([]byte, len(shards[i]))
		copy(cp, shards[i])
		work[i] = cp
	}
	if err := e.Reconstruct(work); err == nil {
		t.Fatal("Reconstruct with k-1 shards: want error, got nil")
	}
}

// TestVerifyValidCodeword checks that freshly encoded shards verify, and that
// Verify catches corruption in a data shard or a parity shard.
func TestVerify(t *testing.T) {
	p := DefaultParams()
	e, _ := EncoderFor(p)
	shards, _ := e.Encode(randBytes(2048, 7))

	ok, err := e.Verify(shards)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Verify(valid) = false, want true")
	}

	for _, shard := range []int{0, 5} { // a data shard and a parity shard
		corrupt := make([][]byte, len(shards))
		for i := range shards {
			cp := make([]byte, len(shards[i]))
			copy(cp, shards[i])
			corrupt[i] = cp
		}
		corrupt[shard][0] ^= 0xff
		ok, err := e.Verify(corrupt)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("Verify(corrupted shard %d) = true, want false", shard)
		}
	}
}

// TestEmptyStripe exercises the zero-length edge: k zero-length data shards
// plus parity, reconstruct and join to empty output.
func TestEmptyStripe(t *testing.T) {
	p := DefaultParams()
	e, _ := EncoderFor(p)
	shards, err := e.Encode(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards) != p.ShardCount() {
		t.Fatalf("shards = %d, want %d", len(shards), p.ShardCount())
	}
	if err := e.Reconstruct(shards); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 0)
	if err := e.Join(out, shards, 0); err != nil {
		t.Fatal(err)
	}
}

func TestParamsValidate(t *testing.T) {
	if err := DefaultParams().Validate(); err != nil {
		t.Errorf("DefaultParams Validate: %v", err)
	}
	for _, p := range []Params{
		{DataShards: 0, ParityShards: 2},
		{DataShards: 4, ParityShards: 0},
		{DataShards: 128, ParityShards: 129},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("Validate(%s) = nil, want error", p)
		}
	}
}

func TestEncoderCache(t *testing.T) {
	a, _ := EncoderFor(DefaultParams())
	b, _ := EncoderFor(DefaultParams())
	if a != b {
		t.Error("EncoderFor returned different encoders for identical params")
	}
}
