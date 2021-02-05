package persistcache

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// BoltStorage is a persistent cache using a bolt db. Items are organized with
// the encryption key as the top-level bucket, and then leases and tokens are
// stored in sub buckets.
type BoltStorage struct {
	db        *bolt.DB
	topBucket string
}

// NewBoltStorage opens a new bolt db at the specified file path and returns it
func NewBoltStorage(path string) (*BoltStorage, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		top, err := tx.CreateBucket([]byte("topBucket"))
		if err != nil {
			return fmt.Errorf("failed to create bucket %s: %w", "topBucket", err)
		}
		_, err = top.CreateBucket([]byte(TokenType))
		if err != nil {
			return fmt.Errorf("failed to create token sub-bucket: %w", err)
		}
		_, err = top.CreateBucket([]byte(LeaseType))
		if err != nil {
			return fmt.Errorf("failed to create lease sub-bucket: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	bs := &BoltStorage{
		db:        db,
		topBucket: "topBucket", // TODO(tvoran): set this somewhere else
	}
	return bs, nil
	// TODO(tvoran): defer db.Close() somewhere?
}

// Set an index in bolt storage
func (b *BoltStorage) Set(id string, index []byte, indexType IndexType) error {

	// TODO(tvoran): encrypt here instead of in lease_cache layer?

	return b.db.Update(func(tx *bolt.Tx) error {
		top := tx.Bucket([]byte(b.topBucket))
		if top == nil {
			return fmt.Errorf("bucket %q not found", b.topBucket)
		}
		s := top.Bucket([]byte(indexType))
		if s == nil {
			return fmt.Errorf("bucket %q not found", indexType)
		}
		return s.Put([]byte(id), index)
	})
}

// Delete an index by id from bolt storage
func (b *BoltStorage) Delete(id string) error {
	return b.db.Update(func(tx *bolt.Tx) error {
		top := tx.Bucket([]byte(b.topBucket))
		if top == nil {
			return fmt.Errorf("bucket %q not found", b.topBucket)
		}
		// Since Delete returns a nil error if the key doesn't exist, just call
		// delete in both sub-buckets without checking existence first
		if err := top.Bucket([]byte(TokenType)).Delete([]byte(id)); err != nil {
			return fmt.Errorf("failed to delete %q from token bucket: %w", id, err)
		}
		if err := top.Bucket([]byte(LeaseType)).Delete([]byte(id)); err != nil {
			return fmt.Errorf("failed to delete %q from lease bucket: %w", id, err)
		}
		return nil
	})
}

// GetByType returns a list of stored items of the specified type
func (b *BoltStorage) GetByType(indexType IndexType) ([][]byte, error) {
	returnBytes := [][]byte{}

	err := b.db.View(func(tx *bolt.Tx) error {
		top := tx.Bucket([]byte(b.topBucket))
		if top == nil {
			return fmt.Errorf("bucket %q not found", b.topBucket)
		}
		top.Bucket([]byte(indexType)).ForEach(func(k, v []byte) error {

			// TODO(tvoran): decrypt here instead of in lease_cache?

			returnBytes = append(returnBytes, v)
			return nil
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return returnBytes, nil
}
