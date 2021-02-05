package persistcache

import (
	"fmt"
)

// MapStorage is intended for use in tests, does not encrypt data
type MapStorage struct {
	tokens map[string][]byte
	leases map[string][]byte
}

// NewMapStorage returns new MapStorage
func NewMapStorage() *MapStorage {
	return &MapStorage{
		tokens: map[string][]byte{},
		leases: map[string][]byte{},
	}
}

// Set an index in the mapstorage
func (ms *MapStorage) Set(id string, index []byte, indexType IndexType) error {
	switch indexType {
	case LeaseType:
		ms.leases[id] = index
	case TokenType:
		ms.tokens[id] = index
	default:
		return fmt.Errorf("unknown index type %q", indexType)
	}

	return nil
}

// Delete an index by id from mapstorage
func (ms *MapStorage) Delete(id string) error {
	if _, ok := ms.leases[id]; ok {
		delete(ms.leases, id)
		return nil
	}
	if _, ok := ms.tokens[id]; ok {
		delete(ms.tokens, id)
		return nil
	}

	return fmt.Errorf("index %q not found in storage", id)
}

// GetByType returns a list of stored items by type
func (ms *MapStorage) GetByType(indexType IndexType) ([][]byte, error) {
	returnBytes := [][]byte{}

	switch indexType {
	case LeaseType:
		for _, v := range ms.leases {
			returnBytes = append(returnBytes, v)
		}
	case TokenType:
		for _, v := range ms.tokens {
			returnBytes = append(returnBytes, v)
		}
	default:
		return nil, fmt.Errorf("unknown index type %q", indexType)
	}

	return returnBytes, nil
}
