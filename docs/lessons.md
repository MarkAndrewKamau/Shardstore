# Lessons Learned

The honest record of what broke, what we misjudged, and what we'd do differently. Entries are added as phases land — see [PHASES.md](PHASES.md) for the phase each batch belongs to.

## Phase 1 batch — EC realities

### 1. Library APIs drift; check the docs before coding

**What we assumed:** `klauspost/reedsolomon`'s `Reconstruct(shards, trim bool)` and `Join(dst []byte, ...)` signatures from memory.

**What actually happened:** v1.14.2 uses `Reconstruct(shards)` (no trim flag) and `Join(dst io.Writer, ...)`. First compile failed with signature errors; `go doc` showed the real API. Also `Split` refuses empty input and `Reconstruct` errors "no shard data" on all-empty shards.

**Impact:** a few minutes of churn plus an empty-stripe special case in `ec.Encode`/`Reconstruct`.

**What we'd do differently:** `go doc <pkg> Encoder` before writing wrappers; add the empty-input edge cases to tests first (they surfaced immediately and shaped the design).

### 2. Empty objects are a real edge case, not a footnote

**What we assumed:** size-0 objects would fall out naturally.

**What actually happened:** zero-length input breaks the RS split, and object size/last-stripe math assumes at least one stripe. We needed a per-object marker file (`.meta`) to distinguish "empty object" from "object doesn't exist" — this is exactly the job the Phase 3 metadata service will take over.

**Impact:** `ObjectStore` grew a marker concept and `ShardStore` interface methods. S3 `PutObject` with 0 bytes (Phase 2) hits this immediately.

**What we'd do differently:** model empty objects as a first-class case in the storage interface from the start.

### 3. RS `Verify` is a fast check, not an integrity guarantee

**What we assumed:** could lean on `Verify` for corruption detection.

**What actually happened:** RS parity verification has theoretical false negatives (a corrupted byte can coincidentally satisfy the parity equation) and costs the same as encoding.

**Impact:** we made per-shard SHA-256 authoritative: stored in the shard header, checked on every read and scrub. `Verify` remains a cheap sanity check, not the durability story. Bit-rot is detected by checksum, not by parity.

### 4. Split shares memory with the input

**What we assumed:** `enc.Split` copies.

**What actually happened:** data shards are slices of the input's padded buffer. Mutating the source after `Encode` corrupts the shards; reconstructing in place mutates the passed slice.

**Impact:** documented contract on `Encode`/`Reconstruct` ("do not mutate after encode"); tests deep-copy before reconstructing. This matters later when streaming (Phase 4) reuses stripe buffers.

### 5. Encoding is not the bottleneck

**What we assumed:** EC would be CPU-heavy.

**What actually happened:** ~3.5 GB/s encode and ~8.5 GB/s single-shard reconstruct per core (SIMD). For a 1 GbE cluster the network, not the CPU, will be the wall.

**Impact:** reprioritized Phase 4 attention to write fan-out and read pipelining, not to CPU tuning.

## Phase 6 batch (failure-behavior surprises)

*To be written on completion of Phase 6.*

---

## Format for new entries

```
### Date — Phase N

**What we assumed:** ...
**What actually happened:** ...
**Impact:** ...
**What we'd do differently:** ...
```