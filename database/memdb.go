package database

import (
	"github.com/hashicorp/go-memdb"
)

var memDB *memdb.MemDB

// Concurrency represents the concurrency count for a specific m3uIndex
type Concurrency struct {
	M3UIndex int
	Count    int
}

// InitializeMemDB initializes the in-memory database
func InitializeMemDB() error {
	// Create the DB schema
	schema := &memdb.DBSchema{
		Tables: map[string]*memdb.TableSchema{
			"concurrency": {
				Name: "concurrency",
				Indexes: map[string]*memdb.IndexSchema{
					"m3uIndex": {
						Name:    "m3uIndex",
						Unique:  true,
						Indexer: &memdb.IntFieldIndex{Field: "M3UIndex"},
					},
				},
			},
		},
	}

	// Create a new data base
	db, err := memdb.NewMemDB(schema)
	if err != nil {
		return err
	}

	memDB = db
	return nil
}

// GetConcurrency retrieves the concurrency count for the given m3uIndex
func GetConcurrency(m3uIndex int) (int, error) {
	txn := memDB.Txn(false)
	defer txn.Abort()

	raw, err := txn.First("concurrency", "m3uIndex", m3uIndex)
	if err != nil {
		return 0, err
	}

	if raw == nil {
		return 0, nil // Key does not exist
	}

	return raw.(*Concurrency).Count, nil
}

// IncrementConcurrency increments the concurrency count for the given m3uIndex
func IncrementConcurrency(m3uIndex int) error {
	txn := memDB.Txn(true)
	defer txn.Commit()

	raw, err := txn.First("concurrency", "m3uIndex", m3uIndex)
	if err != nil {
		return err
	}

	var count int
	if raw != nil {
		count = raw.(*Concurrency).Count
	}

	count++

	err = txn.Insert("concurrency", &Concurrency{M3UIndex: m3uIndex, Count: count})
	if err != nil {
		return err
	}

	return nil
}

// DecrementConcurrency decrements the concurrency count for the given m3uIndex
func DecrementConcurrency(m3uIndex int) error {
	txn := memDB.Txn(true)
	defer txn.Commit()

	raw, err := txn.First("concurrency", "m3uIndex", m3uIndex)
	if err != nil {
		return err
	}

	var count int
	if raw != nil {
		count = raw.(*Concurrency).Count
	}

	count--

	err = txn.Insert("concurrency", &Concurrency{M3UIndex: m3uIndex, Count: count})
	if err != nil {
		return err
	}

	return nil
}

