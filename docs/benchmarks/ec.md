# EC benchmarks — Phase 1

Measured 2026-08-19 with `go test -bench=. -benchmem -run=XXX ./internal/ec/`.

## Hardware / software

| | |
|---|---|
| CPU | Intel Core i7-8565U (8 threads, ~1.8 GHz base) |
| RAM | 15 GiB |
| Kernel | 6.7.1-100.fc43.x86_64 |
| Go | 1.26.2 |
| reedsolomon | github.com/klauspost/reedsolomon v1.14.2 |

## Results

Stripe size: 8 MiB (the default). `ns/op` per stripe; `MB/s` on the original stripe bytes.

| Benchmark | ns/op | MB/s | B/op | allocs/op |
|-----------|------:|-----:|-----:|----------:|
| Encode 4+2 | 2,393,914 | 3,504 | 4,203,667 | 15 |
| Encode 6+3 | 2,530,540 | 3,315 | 5,596,842 | 15 |
| Reconstruct 4+2 (1 shard lost) | 990,249 | 8,471 | 2,106,416 | 15 |
| Reconstruct 6+3 (1 shard lost) | 1,217,588 | 6,890 | 1,402,127 | 16 |
| Verify 4+2 | 2,523,597 | 3,324 | 4,203,426 | 13 |
| Verify 6+3 | 2,172,229 | 3,862 | 4,203,494 | 13 |

## Interpretation

- **Encode ≈ 3.5 GB/s per core.** The reedsolomon SIMD path is not the bottleneck:
  a single core encodes 8 MiB in ~2.4 ms. At 4+2 the *network* fan-out of 6 shards
  will dominate writes long before CPU does (see Phase 4 numbers).
- **Reconstruct is ~2.4x encode.** Rebuilding one lost shard only re-derives
  that shard from k survivors — cheaper than a full encode of k+m shards.
- **Verify ≈ encode cost** — expected; it re-derives all parity.
- **Memory: ~4 MiB/stripe during encode** — the RS split's padded buffer, i.e.
  ~1 stripe of working memory. Streaming at stripe granularity bounds memory
  regardless of object size (a 1 TiB object costs the same 4 MiB).

## Cost model inputs

Capacity efficiency (logical → raw, per stripe):

| Scheme | Raw bytes / logical byte | Tolerated loss |
|--------|--------------------------|----------------|
| 4+2 | 1.5x | 2 shards |
| 6+3 | 1.5x | 3 shards |
| 3x replication | 3.0x | 2 replicas |

At 4+2, rebuilding a lost shard requires reading ~k× its size from survivors
(read amplification 4x for a single-shard repair); rebuild throughput is
therefore bounded by survivors' read bandwidth, not by encode speed.