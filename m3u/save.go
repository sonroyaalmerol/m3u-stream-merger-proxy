package m3u

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func saveToJSON(streams []StreamInfo) error {
	filename := filepath.Join(".", "data", "database.json")
	data, err := json.MarshalIndent(streams, "", "    ")
	if err != nil {
		return fmt.Errorf("error marshalling to JSON: %v", err)
	}

	err = os.WriteFile(filename, data, 0644)
	if err != nil {
		return fmt.Errorf("error writing JSON file: %v", err)
	}

	return nil
}

func loadFromJSON() ([]StreamInfo, error) {
	filename := filepath.Join(".", "data", "database.json")
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading JSON file: %v", err)
	}

	var streams []StreamInfo
	err = json.Unmarshal(data, &streams)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling from JSON: %v", err)
	}

	return streams, nil
}
