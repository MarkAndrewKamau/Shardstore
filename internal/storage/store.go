package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ShardStore persists and retrieves individual shards. Implementations must
// return ErrCorruptShard when a shard's payload fails its checksum, so
// bit-rot surfaces as a typed error rather than silent bad data.
//
// Markers are per-object metadata files that distinguish empty objects from
// nonexistent ones; the Phase 3 metadata service supersedes them.
type ShardStore interface {
	PutShard(ctx context.Context, key ShardKey, h ShardHeader, data []byte) error
	GetShard(ctx context.Context, key ShardKey) ([]byte, ShardHeader, error)
	// VerifyShard streams the payload, hashing it without loading it into
	// memory. Returns the header, or ErrCorruptShard.
	VerifyShard(ctx context.Context, key ShardKey) (ShardHeader, error)
	DeleteShard(ctx context.Context, key ShardKey) error
	ListShards(ctx context.Context) ([]ShardKey, error)
	ListObjectShards(ctx context.Context, objectID string) ([]ShardKey, error)
	WriteMarker(ctx context.Context, objectID string, meta ObjectMeta) error
	ReadMarker(ctx context.Context, objectID string) (ObjectMeta, error)
	RemoveMarker(ctx context.Context, objectID string) error
}

// FileStore persists shards as files under root/objects/<hash(objectID)>/.
// Each shard file is the binary header followed by its payload. Writes are
// fsynced before returning to make the durability claim real.
type FileStore struct {
	root string
}

// NewFileStore creates a FileStore rooted at root (created if missing).
func NewFileStore(root string) (*FileStore, error) {
	dir := filepath.Join(root, "objects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	return &FileStore{root: dir}, nil
}

// objectDir returns the on-disk directory for an object. The directory name
// is derived from the object ID hash so arbitrary S3 keys are safe on disk.
func (f *FileStore) objectDir(objectID string) string {
	sum := sha256.Sum256([]byte(objectID))
	return filepath.Join(f.root, hex.EncodeToString(sum[:20]))
}

func shardFileName(key ShardKey) string {
	return fmt.Sprintf("%08x-%d.shard", key.StripeIdx, key.ShardIdx)
}

func (f *FileStore) shardPath(key ShardKey) string {
	return filepath.Join(f.objectDir(key.ObjectID), shardFileName(key))
}

func (f *FileStore) PutShard(ctx context.Context, key ShardKey, h ShardHeader, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key.ObjectID != h.ObjectID {
		return fmt.Errorf("storage: key/header object id mismatch")
	}
	if int64(len(data)) != h.DataLen {
		return fmt.Errorf("storage: payload len %d != header DataLen %d", len(data), h.DataLen)
	}
	h.Checksum = sha256.Sum256(data)

	dir := f.objectDir(key.ObjectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: mkdir: %w", err)
	}
	path := f.shardPath(key)

	hdrBytes, err := encodeShardHeader(h)
	if err != nil {
		return err
	}
	fh, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("storage: create shard: %w", err)
	}
	defer func() { _ = fh.Close() }()

	if _, err := fh.Write(hdrBytes); err != nil {
		return fmt.Errorf("storage: write header: %w", err)
	}
	if _, err := fh.Write(data); err != nil {
		return fmt.Errorf("storage: write payload: %w", err)
	}
	if err := fh.Sync(); err != nil {
		return fmt.Errorf("storage: fsync shard: %w", err)
	}
	return nil
}

func (f *FileStore) GetShard(ctx context.Context, key ShardKey) ([]byte, ShardHeader, error) {
	fh, err := os.Open(f.shardPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ShardHeader{}, ErrShardNotFound
		}
		return nil, ShardHeader{}, fmt.Errorf("storage: open shard: %w", err)
	}
	defer func() { _ = fh.Close() }()

	h, err := decodeShardHeader(fh)
	if err != nil {
		return nil, ShardHeader{}, fmt.Errorf("storage: decode header: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(fh, h.DataLen))
	if err != nil {
		return nil, ShardHeader{}, fmt.Errorf("storage: read payload: %w", err)
	}
	if int64(len(data)) != h.DataLen {
		return nil, ShardHeader{}, fmt.Errorf("storage: short payload: got %d, want %d", len(data), h.DataLen)
	}
	if sum := sha256.Sum256(data); sum != h.Checksum {
		return nil, h, ErrCorruptShard
	}
	return data, h, nil
}

func (f *FileStore) VerifyShard(ctx context.Context, key ShardKey) (ShardHeader, error) {
	fh, err := os.Open(f.shardPath(key))
	if err != nil {
		if os.IsNotExist(err) {
			return ShardHeader{}, ErrShardNotFound
		}
		return ShardHeader{}, fmt.Errorf("storage: open shard: %w", err)
	}
	defer func() { _ = fh.Close() }()

	h, err := decodeShardHeader(fh)
	if err != nil {
		return ShardHeader{}, fmt.Errorf("storage: decode header: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(fh, h.DataLen)); err != nil {
		return ShardHeader{}, fmt.Errorf("storage: hash payload: %w", err)
	}
	var sum [32]byte
	copy(sum[:], hasher.Sum(nil))
	if sum != h.Checksum {
		return h, ErrCorruptShard
	}
	return h, nil
}

func (f *FileStore) DeleteShard(ctx context.Context, key ShardKey) error {
	path := f.shardPath(key)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrShardNotFound
		}
		return fmt.Errorf("storage: remove shard: %w", err)
	}
	return nil
}

func (f *FileStore) ListShards(ctx context.Context) ([]ShardKey, error) {
	var keys []ShardKey
	err := filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".shard") {
			return nil
		}
		key, err := parseShardFileName(d.Name())
		if err != nil {
			return err
		}
		// The object ID is not derivable from the (hashed) directory name,
		// so resolve it from the shard header itself.
		key.ObjectID, err = resolveObjectID(path)
		if err != nil {
			return err
		}
		keys = append(keys, key)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: walk: %w", err)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ObjectID != keys[j].ObjectID {
			return keys[i].ObjectID < keys[j].ObjectID
		}
		if keys[i].StripeIdx != keys[j].StripeIdx {
			return keys[i].StripeIdx < keys[j].StripeIdx
		}
		return keys[i].ShardIdx < keys[j].ShardIdx
	})
	return keys, nil
}

// resolveObjectID reads just the header of a shard file to recover the
// object ID it belongs to.
func resolveObjectID(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("storage: open shard for id resolution: %w", err)
	}
	defer func() { _ = fh.Close() }()
	h, err := decodeShardHeader(fh)
	if err != nil {
		return "", fmt.Errorf("storage: decode header for id resolution: %w", err)
	}
	return h.ObjectID, nil
}

func (f *FileStore) WriteMarker(ctx context.Context, objectID string, meta ObjectMeta) error {
	b, err := encodeObjectMeta(meta)
	if err != nil {
		return err
	}
	dir := f.objectDir(objectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: mkdir marker: %w", err)
	}
	path := filepath.Join(dir, markerFileName)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("storage: write marker: %w", err)
	}
	fh, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("storage: reopen marker: %w", err)
	}
	defer func() { _ = fh.Close() }()
	if err := fh.Sync(); err != nil {
		return fmt.Errorf("storage: fsync marker: %w", err)
	}
	return nil
}

func (f *FileStore) ReadMarker(ctx context.Context, objectID string) (ObjectMeta, error) {
	b, err := os.ReadFile(filepath.Join(f.objectDir(objectID), markerFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return ObjectMeta{}, ErrObjectNotFound
		}
		return ObjectMeta{}, fmt.Errorf("storage: read marker: %w", err)
	}
	return decodeObjectMeta(b)
}

func (f *FileStore) RemoveMarker(ctx context.Context, objectID string) error {
	path := filepath.Join(f.objectDir(objectID), markerFileName)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("storage: remove marker: %w", err)
	}
	return nil
}

func (f *FileStore) ListObjectShards(ctx context.Context, objectID string) ([]ShardKey, error) {
	all, err := f.ListShards(ctx)
	if err != nil {
		return nil, err
	}
	var keys []ShardKey
	for _, k := range all {
		if k.ObjectID == objectID {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func parseShardFileName(name string) (ShardKey, error) {
	var key ShardKey
	rest, ok := strings.CutSuffix(name, ".shard")
	if !ok {
		return key, fmt.Errorf("storage: not a shard file: %q", name)
	}
	parts := strings.Split(rest, "-")
	if len(parts) != 2 {
		return key, fmt.Errorf("storage: malformed shard name: %q", name)
	}
	var stripe uint64
	if _, err := fmt.Sscanf(parts[0], "%08x", &stripe); err != nil {
		return key, fmt.Errorf("storage: malformed stripe index: %q", parts[0])
	}
	key.StripeIdx = uint32(stripe)
	if _, err := fmt.Sscanf(parts[1], "%d", &key.ShardIdx); err != nil {
		return key, fmt.Errorf("storage: malformed shard index: %q", parts[1])
	}
	return key, nil
}

// MemStore is an in-memory ShardStore used for fast tests and chaos sims.
type MemStore struct {
	mu      sync.RWMutex
	shards  map[ShardKey]memShard
	markers map[string]ObjectMeta
}

type memShard struct {
	h    ShardHeader
	data []byte
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		shards:  make(map[ShardKey]memShard),
		markers: make(map[string]ObjectMeta),
	}
}

func (m *MemStore) PutShard(ctx context.Context, key ShardKey, h ShardHeader, data []byte) error {
	if key.ObjectID != h.ObjectID {
		return fmt.Errorf("storage: key/header object id mismatch")
	}
	if int64(len(data)) != h.DataLen {
		return fmt.Errorf("storage: payload len %d != header DataLen %d", len(data), h.DataLen)
	}
	h.Checksum = sha256.Sum256(data)
	cp := make([]byte, len(data))
	copy(cp, data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shards[key] = memShard{h: h, data: cp}
	return nil
}

func (m *MemStore) GetShard(ctx context.Context, key ShardKey) ([]byte, ShardHeader, error) {
	m.mu.RLock()
	s, ok := m.shards[key]
	m.mu.RUnlock()
	if !ok {
		return nil, ShardHeader{}, ErrShardNotFound
	}
	if sum := sha256.Sum256(s.data); sum != s.h.Checksum {
		return nil, s.h, ErrCorruptShard
	}
	cp := make([]byte, len(s.data))
	copy(cp, s.data)
	return cp, s.h, nil
}

func (m *MemStore) VerifyShard(ctx context.Context, key ShardKey) (ShardHeader, error) {
	m.mu.RLock()
	s, ok := m.shards[key]
	m.mu.RUnlock()
	if !ok {
		return ShardHeader{}, ErrShardNotFound
	}
	if sum := sha256.Sum256(s.data); sum != s.h.Checksum {
		return s.h, ErrCorruptShard
	}
	return s.h, nil
}

func (m *MemStore) DeleteShard(ctx context.Context, key ShardKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.shards[key]; !ok {
		return ErrShardNotFound
	}
	delete(m.shards, key)
	return nil
}

func (m *MemStore) ListShards(ctx context.Context) ([]ShardKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]ShardKey, 0, len(m.shards))
	for k := range m.shards {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ObjectID != keys[j].ObjectID {
			return keys[i].ObjectID < keys[j].ObjectID
		}
		if keys[i].StripeIdx != keys[j].StripeIdx {
			return keys[i].StripeIdx < keys[j].StripeIdx
		}
		return keys[i].ShardIdx < keys[j].ShardIdx
	})
	return keys, nil
}

func (m *MemStore) ListObjectShards(ctx context.Context, objectID string) ([]ShardKey, error) {
	all, err := m.ListShards(ctx)
	if err != nil {
		return nil, err
	}
	var keys []ShardKey
	for _, k := range all {
		if k.ObjectID == objectID {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *MemStore) WriteMarker(ctx context.Context, objectID string, meta ObjectMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markers[objectID] = meta
	return nil
}

func (m *MemStore) ReadMarker(ctx context.Context, objectID string) (ObjectMeta, error) {
	m.mu.RLock()
	meta, ok := m.markers[objectID]
	m.mu.RUnlock()
	if !ok {
		return ObjectMeta{}, ErrObjectNotFound
	}
	return meta, nil
}

func (m *MemStore) RemoveMarker(ctx context.Context, objectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.markers, objectID)
	return nil
}
