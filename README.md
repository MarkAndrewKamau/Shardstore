# Shardstore

**A from-scratch, multi-node object storage system in Go — erasure-coded, S3-compatible, and built to be broken on purpose.**

Shardstore is a teaching-grade distributed object store that does the hard parts for real: Reed-Solomon erasure coding instead of replication, a Raft-backed metadata service, a CRUSH-like zone-aware placement engine, background repair and scrubbing, and a failure-injection harness that proves recovery under disk loss, node loss, network partitions, and bit-rot.

It is **not** a production MinIO/Ceph replacement. It is an engineering project with production-grade *thinking*: every design decision is documented, measured, and stress-tested. Treat this README as the internal design doc.

> Status: **Phase 0 — foundations, not started.** Tracked in [docs/PHASES.md](docs/PHASES.md).

---

## 1. Why build this?

Object storage is the substrate of modern infrastructure, and the interesting problems all live in the durability layer:

- **Erasure coding** (EC) gives replication-level durability at ~50% of the raw storage cost, but it makes reads, writes, and repair substantially harder.
- **Metadata** must be strongly consistent while the data path wants to move huge byte streams — so you split the two planes.
- **Failure is the feature.** A storage system is only as good as its behavior under node death, disk loss, partition, and bit-rot. Most projects never actually demonstrate recovery; this one will, with numbers.

## 2. Scope

### Goals

| # | Goal |
|---|------|
| 1 | S3-compatible API: Put/Get/Head/Delete, ListObjectsV2, and full multipart uploads, verified against `aws cli` / `s3cmd` / `warp`. |
| 2 | Reed-Solomon erasure coding (4+2 default, 6+3 configurable) integrated at the stripe level, with proven reconstruction from any `m`-shard loss. |
| 3 | Strongly consistent metadata service (in-process Raft) fully separated from the data path. |
| 4 | CRUSH-like placement engine with hierarchical failure domains (zone → node) and configurable EC parameters. |
| 5 | Background repair, scrubbing (bit-rot detection), and rebalancing — with rate limiting and priority scheduling. |
| 6 | Durability proofs: a custom fault injector + chaos test suite covering disk loss, node loss, partition, and corruption, with rebuild-time numbers published. |
| 7 | Prometheus metrics: latency histograms, throughput, repair rates, durability events. |
| 8 | Deployable via Docker Compose and Kubernetes (StatefulSets + Helm). |
| 9 | Benchmarks vs MinIO on the same hardware: IOPS, p99 latency, rebuild time, capacity efficiency, resource usage. |
| 10 | Operational docs: runbooks, capacity planning, cost model — updated as the system evolves. |

### Non-goals (v1)

- No TLS in the S3 API (HTTP only; minio-style `--no-tls` posture). TLS is a stretch goal.
- No versioning, lifecycle, or object tagging.
- No multi-region (we simulate multi-AZ within one cluster).
- No full s3tests conformance (we target a documented compatibility surface, §4.7).
- No tiering, compaction, or advanced GC.
- No dedicated admin UI (metrics + CLI only).

## 3. Architecture

Every node runs every role; the Raft leader additionally acts as metadata manager and coordinates repair.

```
                    ┌───────────────────────────────────────────────┐
                    │                 S3 Client                     │
                    │        (aws cli, s3cmd, warp, apps)          │
                    └──────────────────────┬────────────────────────┘
                                           │ HTTP/1.1, XML, SigV4
                    ┌──────────────────────▼────────────────────────┐
                    │            S3 API Gateway (any node)           │
                    │   routing · auth · multipart orchestration     │
                    └──────┬───────────────────┬────────────────────┘
                           │ metadata (Raft)   │ data (placement)
                    ┌──────▼──────────────┐   ┌▼──────────────────────────┐
                    │  Metadata service    │   │  Placement engine         │
                    │  • Raft log + bbolt  │   │  • CRUSH-like, zone-aware │
                    │  • object index      │   │  • shard → node mapping   │
                    │  • node registry     │   └───┬──────────┬───────────┘
                    └──────┬──────────────┘       │ gRPC shard RPCs
                           │                      ▼           ▼
                    ┌──────▼──────────────┐  ┌──────────┐ ┌──────────┐
                    │  Node (manager)     │  │  Node    │ │  Node    │
                    │  • EC engine        │  │  storage │ │  storage │
                    │  • repair/scrub     │  │  engine  │ │  engine  │
                    │  • rebalance        │  │  (shards │ │  (shards │
                    └─────────────────────┘  │   on fs) │ │   on fs) │
                                            └──────────┘ └──────────┘
```

**Data plane:** an object is split into fixed-size *stripes* (default 8 MiB). Each stripe is Reed-Solomon encoded into `k + m` shards (4+2 by default) and the shards are streamed via gRPC to distinct nodes chosen by the placement engine. Shard files are self-describing: magic, version, object ID, stripe index, shard index, and a SHA-256 checksum.

**Metadata plane:** buckets, object index, multipart state, and the node registry live in a Raft-replicated log with bbolt snapshots. Metadata is linearizable; the data path never touches Raft.

**Control plane:** the leader runs the repair/scrub/rebalance loops, publishing metrics and durability events.

## 4. Design decisions & trade-offs

### 4.1 Erasure coding vs replication

| Scheme | Raw overhead | Tolerated failures | Stored per 100 TiB logical |
|--------|-------------|--------------------|---------------------------|
| 2x replication | 2.0x | 1 | 200 TiB |
| 3x replication | 3.0x | 2 | 300 TiB |
| EC 4+2 | 1.5x | 2 | 150 TiB |
| EC 6+3 | 1.5x | 3 | 150 TiB |

Replication is dead simple but wastes 2x the capacity for the same tolerance. EC 4+2 matches 3x replication's tolerance at half the raw capacity. The price: every write touches `k + m` nodes, reads need `k` of them, and *repair* requires reading `k` shards to rebuild one — so rebuild traffic is amplified by `k`/`m` relative to replication.

EC is implemented with [klauspost/reedsolomon](https://github.com/klauspost/reedsolomon) (SIMD-accelerated). We encode/decode *per stripe* so that encoding is streaming: memory stays bounded regardless of object size.

**Tiny-object note:** we always write full stripes, so EC overhead ratio (1.5x) holds even for small objects — no shard-size floor penalty. (Ceph-style small-object replication is a possible future optimization; not in v1.)

### 4.2 Why stripes?

- Streaming: encode a stripe, fan it out, free the buffer — constant memory.
- Parallelism: `k`+ nodes encode/write/read concurrently.
- Repair granularity: one lost shard = one stripe's shard, not the whole object.
- Multipart uploads map naturally: an S3 part is a whole number of stripes.

### 4.3 Metadata: in-process Raft, not etcd

| | In-process Raft | etcd |
|---|---|---|
| Ops burden | One binary, one process | External system to deploy/operate |
| Coupling | Metadata lifecycle tied to nodes | Decoupled, proven |
| Mini-project fit | Excellent | Adds moving parts we don't need |

We use [hashicorp/raft](https://github.com/hashicorp/raft) with bbolt for log + snapshot storage. Trade-off accepted: metadata availability is tied to cluster health — which is exactly the coupling we want for a CP system (§4.4). The Raft log only sees small metadata records (object index entries, node registry updates), never object bytes.

### 4.4 Consistency model: strong metadata, write-all data (CP)

- **Metadata:** linearizable via Raft. Bucket/object/multipart state is always authoritative.
- **Data writes:** an object is committed only when **all** `k + m` shards are durably acked. A partial write (some shards lost mid-write) is *still reconciled*: the write fails, but background repair heals the orphaned shards — no client retry gymnastics required.
- **Data reads:** read the fastest `k` shards, verify checksum + version, reconstruct. On failure, try more shards; if fewer than `k` are available, fail the read and fire a durability event.

**Trade-off:** writes fail while any node is unreachable (until it's marked down). That's a deliberate availability sacrifice — Ceph-style "commit at quorum, lazily heal" writes are a documented future direction. For a storage system whose selling point is durability, we'd rather fail a write than silently lose data. Read-after-write is always strong: metadata commits after shards are durable.

### 4.5 Placement: consistent hashing → CRUSH-like

- **v1 (Phase 3):** rendezvous hashing on object ID → primary node, with virtual nodes for balance. Simple, no data movement metadata.
- **v2 (Phase 4):** CRUSH-like hierarchical placement: *zone → node → disk*, with a weight-aware allocation that never places two shards of the same stripe on the same node (or same zone when the cluster has ≥ 2 zones).

The placement engine is deterministic (pure function of object ID + cluster topology), so any node can compute shard locations without a lookup — the metadata index only stores object *existence*, not shard maps.

### 4.6 Zone-aware placement (the math)

A stripe survives a full zone loss iff surviving shards ≥ `k`:

```
shards_per_zone = (k + m) / z      (balanced)
survives 1 zone loss  ⇔  (k + m) − ⌈(k + m)/z⌉ ≥ k
```

| Config | Zones | Survives zone loss? |
|--------|-------|---------------------|
| 4+2 (6 shards) | 2 | No (3 < 4 survive) |
| 4+2 | 3 | Yes (4 survive) |
| 6+3 (9 shards) | 3 | Yes (6 survive) |

So zone-awareness isn't free: surviving an AZ outage requires either more parity or more zones. We model this explicitly in placement and in capacity planning (§7), and the failure-injection suite will actually test it.

### 4.7 S3 compatibility surface

| Operation | Status |
|-----------|--------|
| CreateBucket / ListBuckets / DeleteBucket | Phase 2 |
| PutObject / GetObject / HeadObject / DeleteObject | Phase 2 |
| GetObject ranged GET (Range header) | Phase 2 |
| ListObjectsV2 (prefix, delimiter, continuation-token) | Phase 2 |
| CreateMultipartUpload / UploadPart / ListParts / CompleteMultipartUpload / AbortMultipartUpload | Phase 2 |
| Signature V4 (verify; permissive mode for dev) | Phase 2 |
| CopyObject, tagging, lifecycle, ACLs | Non-goal |

Compatibility is proven against real clients (`aws cli`, `s3cmd`, MinIO `warp`), not just our own tests. The 5 MiB minimum part size (non-final part) is enforced, since `aws cli`/`warp` depend on it.

### 4.8 Repair, scrub, rebalance

- **Repair:** the leader compares the object index against actual shard state (presence, version, checksum). Missing/corrupt shards are rebuilt from the fastest `k` survivors and placed per the placement engine. Priority: stripes with fewest surviving shards first. Rate-limited (IO/network budget) so repair doesn't starve client traffic.
- **Scrub:** periodic full-checksum verification of shard files — the only defense against bit-rot (silent media corruption). A bad checksum is treated as shard loss: fire a durability event, repair. Scrub runs throttled in the background.
- **Rebalance:** on node join, new objects use the full ring; existing objects are *not* migrated (v1 trade-off — migration is expensive and risky). On permanent node loss, only the affected objects' stripes are re-repaired, not the whole ring. Full rebalancing is a documented stretch goal.

### 4.9 Transports

| Plane | Protocol | Why |
|-------|----------|-----|
| S3 API | HTTP/1.1 + XML + SigV4 | Interop with real clients |
| Internal shard RPC | gRPC (streaming) | Typed, streaming-friendly, codegen'd |
| Internal metadata | Raft (in-process, loopback) | No separate wire protocol |

### 4.10 Security posture (v1)

- SigV4 request verification (opt-out flag for local dev), no TLS (§2), no authn for internal RPC (trusted network).
- Checksums everywhere: per-shard SHA-256 in the shard header *and* in the object index; verified on read and scrub.
- Documented threat model: v1 trusts the network; encryption at rest / in transit are stretch goals.

## 5. Failure is the feature

| Scenario | Detection | Recovery | Metrics event |
|----------|-----------|----------|---------------|
| **Disk failure** | Shard read error / missing file | Repair rebuilds shard onto a healthy node (placement-aware) | `shard_loss`, `repair_*` |
| **Node loss** | Heartbeat timeout → membership change | Reads reconstruct from `k` survivors; repair re-places shards on remaining nodes | `node_down`, `repair_*` |
| **Leader failure** | Raft election | New leader elected; control loops resume idempotently | `raft_*` |
| **Network partition** | Raft minority → no quorum; data writes fail (CP) | Heal on partition end; no split-brain by construction | `no_quorum` |
| **Bit-rot** | Scrub checksum mismatch | Treated as shard loss → repair | `bitrot_detected` |
| **Corrupt stripe (≥ m+1 shards lost)** | Unreadable object | **Data loss — reported loudly.** Out of scope for v1 (that's what 6+3 / zones are for) | `data_loss` |

Each scenario has a deterministic test in the chaos suite (Phase 6) asserting: availability during the failure, no data loss, and completed recovery with a recorded rebuild time. Results are published in [docs/benchmarks](docs/benchmarks).

## 6. Measurements

Numbers land in `docs/benchmarks/` as phases complete. Planned artifacts:

- Latency: p50/p99 for Put/Get/List, small and large objects.
- Throughput: MB/s and ops/s under `warp` mixed workloads.
- Rebuild time vs object size and cluster size (e.g., 1 GiB object, 4+2, node loss → full rebuild).
- Capacity efficiency: logical vs physical bytes stored (expect 1.5x at 4+2).
- Resource usage: CPU, RSS, FD counts per node; encode/decode MB/s per core.
- Durability events: counts per scenario from the chaos suite.

Benchmark methodology will be published alongside results (same hardware, pinned versions, warm-up, repeats) — comparisons vs MinIO are only meaningful with the methodology.

## 7. Operations

- **Runbooks** (`docs/runbooks/`): node down, disk full, quorum loss, bit-rot, botched upgrade.
- **Capacity planning** (`docs/operations/capacity.md`): node count ≥ `k + m + 1` (headroom for rebuild), zone math from §4.6, MTTR targets.
- **Cost model** (`docs/operations/cost.md`): worked example — 100 TiB logical at EC 4+2 = 150 TiB raw vs 300 TiB at 3x replication; on-prem disk + power vs S3 pricing, with rebuild-bandwidth assumptions.

## 8. Repo layout

```
cmd/shardstore/       # main entrypoint (server, admin subcommands)
internal/
  api/                # S3 API gateway (HTTP, XML, SigV4)
  metadata/           # Raft service, bbolt, object index
  storage/            # shard engine (local fs + in-memory impls)
  ec/                 # stripe encode/decode (reedsolomon wrapper)
  placement/          # consistent hashing → CRUSH-like engine
  repair/             # repair/scrub/rebalance loops
  rpc/                # internal gRPC data path
  metrics/            # Prometheus registry & durability events
  config/  logging/   # plumbing
deploy/
  compose/            # docker-compose profiles (3-node, 6-node, chaos)
  helm/               # StatefulSet chart
  grafana/            # dashboards
docs/
  PHASES.md           # ← roadmap (start here)
  benchmarks/  runbooks/  operations/
test/
  integration/  chaos/  bench/
```

## 9. Quickstart

*Filled in as Phase 0 lands. Target UX:*

```bash
make build
docker compose -f deploy/compose/6-node.yaml up -d
aws --endpoint-url http://localhost:9000 s3 mb s3://demo
```

## 10. Roadmap

Nine phases, each with acceptance criteria and doc updates: [docs/PHASES.md](docs/PHASES.md).

```
Phase 0 Foundations ▸ 1 Storage+EC ▸ 2 S3 API ▸ 3 Metadata ▸ 4 Distributed data path
▸ 5 Repair/scrub/rebalance ▸ 6 Chaos & durability proofs ▸ 7 Observability
▸ 8 Deployment ▸ 9 Benchmarks & narrative polish
```

## 11. Lessons learned (WIP)

This section grows with the project — the honest record of what broke, what we misjudged, and what we'd do differently. First entries land at the end of Phase 1 (EC realities) and Phase 6 (failure behavior surprises).

---

*License: TBD. Status, metrics, and diagrams update as phases land.*