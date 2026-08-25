# Shardstore — Phase Plan

Docs-first project. Every phase has a goal, explicit deliverables, acceptance criteria, and a list of docs that get updated when it lands. **A phase is done only when its acceptance criteria pass and its docs are updated.**

Legend: `M` = milestone, `S` = stretch goal, `A` = acceptance test.

---

## Phase 0 — Foundations ✅ (complete)

**Goal:** Empty repo → buildable, linted, tested skeleton with CI and a clear layout.

**Deliverables**
- [x] `go.mod` — module `shardstore`; set the full remote path when the GitHub repo is published
- [x] Layout: `cmd/shardstore`, `internal/{api,metadata,storage,ec,placement,repair,rpc,metrics,config,logging,version}`, `test`, `deploy`, `docs`
- [x] `cmd/shardstore`: `server` subcommand with env/flag config (`SHARDSTORE_*` env, flags win); `version` subcommand with ldflags build info
- [x] Structured logging (slog) with request-id plumbing; access log + `X-Request-Id` echo middleware
- [x] `Makefile`: `build`, `test`, `test-race`, `lint`, `bench`, `clean`
- [x] `.golangci.yml` (v2 config); GitHub Actions CI (lint + `go test -race ./...` + build on push/PR)
- [x] Health endpoint `GET /healthz` (in-memory); graceful shutdown on SIGINT/SIGTERM

**Acceptance**
- [x] `make lint` green (golangci-lint v2.12, 0 issues)
- [x] `make test-race` green (config, api, version suites)
- [x] `make build` produces `bin/shardstore`; `shardstore version` prints semver + build info
- [x] Live smoke test: health endpoint + request IDs verified via curl

**Docs updated:** README §8 (layout with phase labels), §9 (quickstart), status line.

---

## Phase 1 — Storage engine + erasure coding core ✅ (complete)

**Goal:** On a single node, store objects as EC'd stripes with checksums, and *prove* reconstruction.

**Deliverables**
- [x] Stripe model: object → stripes (default 8 MiB) → `k+m` shards (4+2 default, 6+3 configurable)
- [x] Self-describing shard file format: magic, version, object ID, stripe index, shard index, SHA-256 (in `internal/storage/header.go`)
- [x] `internal/ec`: streaming per-stripe encode/decode via `klauspost/reedsolomon` v1.14.2; bounded memory (one stripe in flight)
- [x] `internal/storage`: `ShardStore` interface (`Put/Get/Verify/Delete/List` + markers) with `FileStore` (fsync'd writes, hashed dir names) and `MemStore` (tests/chaos)
- [x] `ObjectStore`: streaming PutObject/GetObject/DeleteObject with verify-on-read; corrupted shards treated as lost and reconstructed from survivors
- [x] Fault-matrix tests: every subset of 4+2 and 6+3 shards; object-level shard-loss matrix; bit-rot detection; unrecoverable-object errors; on-disk format roundtrip
- [x] Encode/decode throughput micro-benchmarks → `docs/benchmarks/ec.md`

**Acceptance**
- [x] All fault-matrix subsets pass: any 2 (4+2) or 3 (6+3) lost shards → full reconstruction
- [x] Corrupted shard byte → `ErrCorruptShard`, never returned as data
- [x] Benchmark results recorded in `docs/benchmarks/ec.md`: encode ~3.5 GB/s, single-shard reconstruct ~8.5 GB/s per core

**Docs updated:** README §4.1–4.2 (links to measured numbers), §5 (bit-rot row), §6; `docs/lessons.md` (5 entries); `docs/benchmarks/ec.md` (methodology + results).

---

## Phase 2 — S3 API (single node) ✅ (complete)

**Goal:** A single-node cluster exposes the full target S3 surface; real clients work against it.

**Deliverables**
- [x] HTTP server: XML request/response formats, error XML (S3-style codes), `Range` GET
- [x] Buckets: Create/List/Delete (bucket = lightweight namespace in metadata)
- [x] Objects: Put/Get/Head/Delete, ListObjectsV2 (prefix, delimiter, continuation-token)
- [x] Multipart: CreateMultipartUpload / UploadPart / ListParts / Complete / Abort; 5 MiB non-final part minimum enforced
- [x] SigV4 verification + permissive-mode flag for dev
- [ ] Integration tests against real clients: `aws cli` (`mb/cp/ls/rm --recursive`, multipart via part-size), `s3cmd`
- [ ] `curl`-driven XML conformance tests

**Acceptance**
- [ ] `aws cli` full round-trip incl. multipart (object larger than part size) passes against a single node
- [ ] `warp mixed` runs and produces numbers (recorded, not gated)
- [ ] Integration job in CI

**Docs updated:** README §4.7 (compatibility table), §9 quickstart.

---

## Phase 3 — Metadata service + cluster membership

**Goal:** A 3-node cluster with strongly consistent metadata and client-side routing.

**Deliverables**
- [ ] In-process Raft (hashicorp/raft): log + bbolt snapshots; leader election; linearizable reads
- [ ] Metadata schema: buckets, objects (size, etag, stripe count, version, timestamps), multipart state, node registry (id, addr, zone, disks, capacity, state)
- [ ] Node join protocol (seed peer list), heartbeats, membership states: `active / down / draining`
- [ ] Rendezvous (consistent) hashing ring for object → primary; ring rebuilt from registry
- [ ] S3 gateway on any node routes internally (proxy or direct) to the owning node
- [ ] Failure tests: kill leader → election → writes continue; restart node → state reconciled from log

**Acceptance**
- [ ] 3-node cluster (compose): leader kill mid-write loses no committed metadata
- [ ] Metadata survives full restart; ring is identical post-restart
- [ ] Metadata latency measured (p50/p99) and recorded

**Docs updated:** README §3 (diagram), §4.3–4.4; `docs/runbooks/quorum-loss.md`.

---

## Phase 4 — Distributed data path + zone-aware placement

**Goal:** Objects are EC'd across nodes with CRUSH-like placement; reads reconstruct on the fly.

**Deliverables**
- [ ] Internal gRPC data path: shard Put/Get/Delete with streaming + checksum verification
- [ ] CRUSH-like placement engine: `zone → node → disk` hierarchy, weight-aware, no two shards of a stripe on one node (or one zone, when possible)
- [ ] Write path: encode per stripe → fan out shards to `k+m` nodes → commit metadata only after all acks
- [ ] Partial-write reconciliation: orphaned shards healed by repair (write still fails to client)
- [ ] Read path: fastest-`k` fetch, checksum+version verify, reconstruct; retry with more shards on failure; fail + durability event when < `k` available
- [ ] Zone-awareness: configurable `zone` label per node; stripe spread across ≥ 2 zones when possible; §4.6 math enforced
- [ ] Node-loss test: kill a node mid-traffic → reads keep working via reconstruction

**Acceptance**
- [ ] 6-node compose cluster; put/get round-trip through gRPC data path
- [ ] Kill 1 node with live read/write traffic → zero failed reads, zero data loss
- [ ] Placement invariant property-test: no stripe has two shards on the same node/zone
- [ ] Latency p99 recorded for read/write across 6 nodes

**Docs updated:** README §3, §4.4–4.6, §5 (node loss / partition rows); `docs/operations/capacity.md` zone math.

---

## Phase 5 — Repair, scrub, rebalance

**Goal:** The system heals itself; bit-rot is found and repaired.

**Deliverables**
- [ ] Repair loop (leader-driven): reconcile object index vs actual shard state; rebuild from fastest `k` survivors; placement-aware target selection; priority (fewest survivors first); rate-limited (IO/network budget)
- [ ] Scrubbing: throttled full-checksum verification; bit-rot → durability event + repair
- [ ] Membership-driven repair: node `down` → affected stripes repaired; node rejoin → ring updates, no mass migration (v1 trade-off)
- [ ] Repair metrics: in-flight shards, rate (shards/hour), rebuild duration histogram
- [ ] Idempotency: repair restarts safely mid-run; concurrent leaders can't double-schedule (guard on Raft)

**Acceptance**
- [ ] Kill node holding ≥ 2 objects → repair completes → all objects intact; rebuild time recorded
- [ ] Kill node *during* repair → repair resumes, no corruption
- [ ] Inject bit-rot byte flips → scrub detects, repairs, event fired
- [ ] Repair respects rate budget (client latency p99 stable during repair)

**Docs updated:** README §4.8, §5; `docs/runbooks/{node-down,bit-rot}.md`; `docs/benchmarks/rebuild.md`.

---

## Phase 6 — Failure injection + durability proofs

**Goal:** Demonstrate — with numbers and tests — exactly what happens under every failure class.

**Deliverables**
- [ ] Custom fault injector (`cmd/chaos` + Go harness): kill-node, partition-node (drop/isolate), corrupt-shard (bit-rot), disk-fail (remove shard dir), traffic soak with injected faults, random fault sweeps
- [ ] Deterministic scenario tests asserting: availability during fault, no data loss for ≤ `m` faults, `data_loss` event when > `m` (reported, not hidden), recovery completes
- [ ] Zone-loss scenario (kill a whole zone) with the §4.6 configs (prove the math)
- [ ] CI job: chaos suite on compose cluster (nightly full matrix, on-demand fast smoke)
- [ ] Optional: Chaos Mesh manifests for the K8s deployment (S)

**Acceptance**
- [ ] Full failure matrix passes; every scenario publishes a rebuild-time row
- [ ] Metrics events asserted in tests (e.g., `bitrot_detected`, `shard_loss`, `repair_done`)
- [ ] Rebuild-time table committed to `docs/benchmarks/`

**Docs updated:** README §5 (full table), §6; `docs/lessons.md` (second batch).

---

## Phase 7 — Observability

**Goal:** Every claim is visible on a dashboard.

**Deliverables**
- [ ] Prometheus `/metrics`: latency histograms (Put/Get/List, object-size buckets), throughput, repair rate + rebuild duration, durability events (shard loss, bit-rot, node down, data loss), per-node capacity/usage, EC efficiency (logical vs physical), Raft metrics
- [ ] Grafana dashboard JSON in `deploy/grafana/`
- [ ] Structured logging (slog) with request IDs; health endpoint; readiness (quorum) endpoint
- [ ] Optional: OTLP traces (S)

**Acceptance**
- [ ] `docker compose` up → dashboards populated from live cluster
- [ ] Chaos run produces dashboard-visible repair/durability panels
- [ ] Metric names + units documented in README §6

**Docs updated:** README §6 (metric table); `docs/operations/capacity.md`.

---

## Phase 8 — Deployment

**Goal:** One command to a 3- or 6-node cluster; same on Kubernetes.

**Deliverables**
- [ ] Docker Compose profiles: `3-node` (fast dev), `6-node` (EC + zones), `chaos` (with fault-injector sidecar), plus prometheus+grafana
- [ ] Kubernetes: StatefulSet + headless service, local-path/standard storage class, probes (liveness/readiness)
- [ ] Helm chart with values: node count, EC params (`k`, `m`), zone labels, storage class, resource limits
- [ ] CI: compose chaos job runs on every PR (fast smoke) + nightly full matrix
- [ ] Optional: Terraform for bare-metal/VM launch (S)

**Acceptance**
- [ ] `docker compose -f deploy/compose/6-node.yaml up` → aws cli round-trip + warp numbers
- [ ] `helm install` on kind/minikube → same assertions pass
- [ ] Upgrade path documented (rolling restart of a StatefulSet with no data loss)

**Docs updated:** README §8 (layout), §9; `docs/runbooks/upgrade.md`; `docs/operations/capacity.md`.

---

## Phase 9 — Benchmarks, docs, narrative polish

**Goal:** Honest, reproducible numbers and a README that reads like a design doc.

**Deliverables**
- [ ] Benchmarks vs MinIO on identical hardware (methodology published): warp mixed/read/write, p99 latency, IOPS, rebuild-time comparison, capacity efficiency table, CPU/mem/FD per node
- [ ] Bench scripts + raw results committed; graphs rendered into README/docs
- [ ] Runbooks complete: node down, disk full, quorum loss, bit-rot, botched upgrade
- [ ] Capacity planning + cost model final numbers (worked examples from real measurements)
- [ ] README polish: architecture diagram refresh, Lessons learned final, talk/blog-post draft (S)
- [ ] Optional: xDS-style dynamic config / Infraforge control-plane integration spike (S)

**Acceptance**
- [ ] Reproducible: `make bench` on a fresh machine reproduces published graphs
- [ ] Every failure scenario in §5 has a documented, measured outcome
- [ ] Docs review: README + runbooks read coherently as an internal design doc

---

## Stretch goals (tracked separately)

- Multi-AZ simulation (fault domains beyond zones, e.g., power/network dependencies)
- Full `s3tests` conformance push
- Ceph/MinIO comparisons on rebuild, small-object replication optimization
- TLS for S3 API + internal RPC, encryption at rest
- Quorum-commit writes (Ceph-style lazy repair) as an availability knob
- Read caching, tiering (hot/cold), object versioning/lifecycle
- xDS-style dynamic config / Infraforge control-plane integration

---

## Cross-cutting conventions

- **Race detector on by default** in CI (`go test -race ./...`).
- **Property/fault-matrix tests** wherever invariants exist (placement, EC reconstruction, membership).
- **Every number published** in `docs/benchmarks/` with methodology.
- **Docs update with the code, not after** — acceptance criteria include the doc updates above.
- **No commits of binaries, data dirs, or secrets** (see `.gitignore`).