package ec

import (
	"testing"
)

const benchStripeSize = 8 << 20 // 8 MiB, matching the default stripe size

func benchEncode(b *testing.B, p Params) {
	b.Helper()
	data := randBytes(benchStripeSize, 42)
	e, err := EncoderFor(p)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(benchStripeSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Encode(data); err != nil {
			b.Fatal(err)
		}
	}
}

func benchReconstruct(b *testing.B, p Params) {
	b.Helper()
	data := randBytes(benchStripeSize, 42)
	e, err := EncoderFor(p)
	if err != nil {
		b.Fatal(err)
	}
	shards, err := e.Encode(data)
	if err != nil {
		b.Fatal(err)
	}
	work := make([][]byte, len(shards))
	b.SetBytes(benchStripeSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range shards {
			if j == 0 {
				work[j] = nil // simulate one lost shard
			} else {
				work[j] = shards[j]
			}
		}
		if err := e.Reconstruct(work); err != nil {
			b.Fatal(err)
		}
	}
}

func benchVerify(b *testing.B, p Params) {
	b.Helper()
	data := randBytes(benchStripeSize, 42)
	e, err := EncoderFor(p)
	if err != nil {
		b.Fatal(err)
	}
	shards, err := e.Encode(data)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(benchStripeSize)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Verify(shards); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeStripe4x2(b *testing.B) { benchEncode(b, Params{4, 2}) }
func BenchmarkEncodeStripe6x3(b *testing.B) { benchEncode(b, Params{6, 3}) }
func BenchmarkReconstruct4x2(b *testing.B)  { benchReconstruct(b, Params{4, 2}) }
func BenchmarkReconstruct6x3(b *testing.B)  { benchReconstruct(b, Params{6, 3}) }
func BenchmarkVerify4x2(b *testing.B)       { benchVerify(b, Params{4, 2}) }
func BenchmarkVerify6x3(b *testing.B)       { benchVerify(b, Params{6, 3}) }
