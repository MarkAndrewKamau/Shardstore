package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func testParams() ECParams { return ECParams{DataShards: 4, ParityShards: 2} }

func randData(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

func newTestStore(t *testing.T) ShardStore {
	t.Helper()
	return NewMemStore()
}

func newTestObjectStore(t *testing.T, store ShardStore) *ObjectStore {
	t.Helper()
	return NewObjectStore(store, testParams(), 1<<10)
}

func putObject(t *testing.T, o *ObjectStore, id string, data []byte) {
	t.Helper()
	if err := o.PutObject(context.Background(), id, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("PutObject(%s): %v", id, err)
	}
}

func getObject(t *testing.T, o *ObjectStore, id string) []byte {
	t.Helper()
	rc, size, err := o.GetObject(context.Background(), id)
	if err != nil {
		t.Fatalf("GetObject(%s): %v", id, err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	if int64(len(got)) != size {
		t.Fatalf("size mismatch: reader gave %d, header says %d", len(got), size)
	}
	return got
}

func TestObjectRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"tiny", 1},
		{"one_shard", 250},                     // < 1 shard of 1024/4
		{"exactly_stripe", 1 << 10},            // boundary
		{"stripe_plus_partial", (1 << 10) + 7}, // last stripe partial
		{"multi_stripe", 5<<10 + 13},
		{"three_stripes_exact", 3 << 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newTestObjectStore(t, newTestStore(t))
			data := randData(tc.size, int64(tc.size)+5)
			putObject(t, o, "obj", data)
			if !bytes.Equal(getObject(t, o, "obj"), data) {
				t.Fatal("roundtrip mismatch")
			}
		})
	}
}

// TestShardLossFaultMatrix proves object-level recovery: delete any subset of
// up to m shards of any stripe and the object still reads back intact. This
// complements the EC-level matrix by exercising the store + reader path.
func TestShardLossFaultMatrix(t *testing.T) {
	p := testParams()
	store := newTestStore(t)
	o := newTestObjectStore(t, store)
	data := randData(3<<10+11, 99) // 3 full stripes + partial

	putObject(t, o, "obj", data)

	total := p.ShardCount()
	for mask := 1; mask < 1<<total; mask++ {
		lost := countBits(mask)
		if lost > p.MaxLoss() {
			continue
		}
		// Delete the masked shards of EVERY stripe.
		for stripe := 0; stripe < 3; stripe++ {
			for i := 0; i < total; i++ {
				if mask&(1<<i) != 0 {
					err := store.DeleteShard(context.Background(), ShardKey{ObjectID: "obj", StripeIdx: uint32(stripe), ShardIdx: uint32(i)})
					if err != nil {
						t.Fatalf("delete shard stripe=%d shard=%d: %v", stripe, i, err)
					}
				}
			}
		}
		got := getObject(t, o, "obj")
		if !bytes.Equal(got, data) {
			t.Fatalf("mask %08b: reconstruction mismatch", mask)
		}
		// Reput the object for the next subset.
		putObject(t, o, "obj", data)
	}
}

func TestBitRotDetectionAndRecovery(t *testing.T) {
	store := newTestStore(t)
	o := newTestObjectStore(t, store)
	data := randData(2<<10+1, 3)

	putObject(t, o, "obj", data)

	// Corrupt one shard of the first stripe in place (silent bit-flip).
	key := ShardKey{ObjectID: "obj", StripeIdx: 0, ShardIdx: 1}
	raw, _, err := store.GetShard(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xff
	corruptInPlace(t, store, key, raw)

	// VerifyShard must detect it.
	if _, err := store.VerifyShard(context.Background(), key); !errors.Is(err, ErrCorruptShard) {
		t.Fatalf("VerifyShard on corrupted shard: err = %v, want ErrCorruptShard", err)
	}

	// Read path treats it as lost and reconstructs from the other 5.
	got := getObject(t, o, "obj")
	if !bytes.Equal(got, data) {
		t.Fatal("read after bit-rot: mismatch")
	}
}

func TestUnrecoverableObject(t *testing.T) {
	p := testParams()
	store := newTestStore(t)
	o := newTestObjectStore(t, store)
	data := randData(1<<10+7, 11)

	putObject(t, o, "obj", data)

	// Lose m+1 = 3 shards of every stripe.
	for stripe := 0; stripe < 2; stripe++ {
		for i := 0; i <= p.MaxLoss(); i++ {
			if err := store.DeleteShard(context.Background(), ShardKey{ObjectID: "obj", StripeIdx: uint32(stripe), ShardIdx: uint32(i)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// GetObject succeeds (it can still compute size); the error surfaces when
	// a stripe cannot be reconstructed during the read.
	rc, _, err := o.GetObject(context.Background(), "obj")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.ReadAll(rc); !errors.Is(err, ErrUnrecoverable) {
		t.Fatalf("read err = %v, want ErrUnrecoverable", err)
	}
}

func TestObjectNotFound(t *testing.T) {
	o := newTestObjectStore(t, newTestStore(t))
	if _, _, err := o.GetObject(context.Background(), "nope"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
	if _, err := o.ObjectShards(context.Background(), "nope"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("ObjectShards err = %v, want ErrObjectNotFound", err)
	}
}

func TestDeleteObject(t *testing.T) {
	o := newTestObjectStore(t, newTestStore(t))
	data := randData(1<<10+3, 17)
	putObject(t, o, "obj", data)

	if err := o.DeleteObject(context.Background(), "obj"); err != nil {
		t.Fatal(err)
	}
	keys, err := o.store.ListShards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("shards remain after delete: %v", keys)
	}
	if _, _, err := o.GetObject(context.Background(), "obj"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound after delete", err)
	}
}

func TestGetObjectStreamsLargeObject(t *testing.T) {
	// Reads 12 stripes through the streaming reader with small chunks,
	// exercising the incremental Read path.
	o := newTestObjectStore(t, newTestStore(t))
	data := randData(12<<10+123, 21)
	putObject(t, o, "obj", data)

	rc, size, err := o.GetObject(context.Background(), "obj")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	if size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", size, len(data))
	}
	var got bytes.Buffer
	chunk := make([]byte, 333)
	for {
		n, err := rc.Read(chunk)
		got.Write(chunk[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("streamed read mismatch")
	}
}

func TestListShards(t *testing.T) {
	store := newTestStore(t)
	o := newTestObjectStore(t, store)
	putObject(t, o, "a", randData(1<<10+1, 1))
	putObject(t, o, "b", randData(500, 2))

	keys, err := store.ListShards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 2 stripes for "a" (1<<10+1 > stripeSize) + 1 stripe for "b" = 3 stripes
	// × 6 shards = 18.
	if len(keys) != 18 {
		t.Fatalf("ListShards = %d keys, want 18", len(keys))
	}
	ids := map[string]int{}
	for _, k := range keys {
		ids[k.ObjectID]++
	}
	if ids["a"] != 12 || ids["b"] != 6 {
		t.Fatalf("per-object shard counts = %v, want a=12 b=6", ids)
	}
}

func TestEmptyObjectMarker(t *testing.T) {
	store := newTestStore(t)
	o := newTestObjectStore(t, store)
	putObject(t, o, "empty", nil)

	got := getObject(t, o, "empty")
	if len(got) != 0 {
		t.Fatalf("empty object read %d bytes, want 0", len(got))
	}
	if _, err := o.ObjectShards(context.Background(), "empty"); err != nil {
		t.Fatalf("ObjectShards on empty object: %v", err)
	}
}

// TestFileStorePersistence exercises the on-disk format: shards survive a
// store reopen, and files are laid out as header+payload.
func TestFileStorePersistence(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	o := NewObjectStore(fs, testParams(), 1<<10)
	data := randData(2<<10+9, 31)
	putObject(t, o, "obj", data)

	// Reopen from the same directory: format must roundtrip.
	fs2, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	o2 := NewObjectStore(fs2, testParams(), 1<<10)
	if !bytes.Equal(getObject(t, o2, "obj"), data) {
		t.Fatal("reopened store: roundtrip mismatch")
	}

	// Every shard file must be header + payload with correct magic.
	var files []string
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".shard" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 18 { // 3 stripes × 6 shards (2<<10+9 bytes at 1<<10/stripe)
		t.Fatalf("shard files = %d, want 18", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) < 8 || !bytes.Equal(b[:8], shardMagic[:]) {
			t.Fatalf("file %s: bad magic", f)
		}
	}
}

// TestFileStoreBitRot verifies the disk store detects in-place corruption.
func TestFileStoreBitRot(t *testing.T) {
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := NewObjectStore(fs, testParams(), 1<<10)
	data := randData(1<<10+5, 41)
	putObject(t, o, "obj", data)

	key := ShardKey{ObjectID: "obj", StripeIdx: 0, ShardIdx: 3}
	path := filepath.Join(fs.objectDir(key.ObjectID), shardFileName(key))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a payload byte (after the header).
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := fs.VerifyShard(context.Background(), key); !errors.Is(err, ErrCorruptShard) {
		t.Fatalf("VerifyShard err = %v, want ErrCorruptShard", err)
	}
	if !bytes.Equal(getObject(t, o, "obj"), data) {
		t.Fatal("read after disk bit-rot: mismatch")
	}
}

// corruptInPlace rewrites a shard's payload without updating its checksum.
func corruptInPlace(t *testing.T, store ShardStore, key ShardKey, raw []byte) {
	t.Helper()
	switch s := store.(type) {
	case *MemStore:
		s.mu.Lock()
		defer s.mu.Unlock()
		e := s.shards[key]
		cp := make([]byte, len(raw))
		copy(cp, raw)
		e.data = cp
		s.shards[key] = e
	default:
		t.Fatalf("no corruption hook for %T", store)
	}
}

func countBits(v int) int {
	n := 0
	for v != 0 {
		n += v & 1
		v >>= 1
	}
	return n
}
