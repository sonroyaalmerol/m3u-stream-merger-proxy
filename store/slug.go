package store

import (
	"bytes"
	"encoding/base32"
	"fmt"
	"io"

	"github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
)

func EncodeSlug(stream StreamInfo) string {
	jsonData, err := json.Marshal(stream)
	if err != nil {
		return ""
	}

	var compressedData bytes.Buffer
	writer, err := zstd.NewWriter(&compressedData)
	if err != nil {
		return ""
	}

	_, err = writer.Write(jsonData)
	if err != nil {
		return ""
	}
	writer.Close()

	encodedData := base32.StdEncoding.EncodeToString(compressedData.Bytes())
	return encodedData
}

func DecodeSlug(encodedSlug string) (*StreamInfo, error) {
	decodedData, err := base32.StdEncoding.DecodeString(encodedSlug)
	if err != nil {
		return nil, fmt.Errorf("error decoding Base32 data: %v", err)
	}

	reader, err := zstd.NewReader(bytes.NewReader(decodedData))
	if err != nil {
		return nil, fmt.Errorf("error creating zstd reader: %v", err)
	}
	decompressedData, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("error reading decompressed data: %v", err)
	}
	reader.Close()

	var result StreamInfo
	err = json.Unmarshal(decompressedData, &result)
	if err != nil {
		return nil, fmt.Errorf("error deserializing data: %v", err)
	}

	return &result, nil
}
