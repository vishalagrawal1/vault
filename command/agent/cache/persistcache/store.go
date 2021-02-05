package persistcache

type IndexType string

const (
	LeaseType IndexType = "lease"
	TokenType           = "token"
)

type Storage interface {
	// Set -
	Set(string, []byte, IndexType) error

	// Delete -
	Delete(id string) error

	// GetByType - return types may change depending on boltdb interface
	GetByType(IndexType) ([][]byte, error)

	// Clear?

	// Rotate key?
}
