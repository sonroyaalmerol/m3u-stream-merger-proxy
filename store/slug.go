package store

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/DataDog/zstd"
	"github.com/goccy/go-json"
)

func EncodeSlug(stream StreamInfo) string {
	jsonData, err := json.Marshal(stream)
	if err != nil {
		return ""
	}

	var compressedData []byte
	_, err = zstd.CompressLevel(compressedData, jsonData, zstd.BestCompression)
	if err != nil {
		return ""
	}

	encodedData := base64.StdEncoding.EncodeToString(compressedData)

	// 62nd char of encoding
	encodedData = strings.Replace(encodedData, "+", "-", -1)
	// 63rd char of encoding
	encodedData = strings.Replace(encodedData, "/", "_", -1)
	// Remove any trailing '='s
	encodedData = strings.Replace(encodedData, "=", "", -1)

	return encodedData
}

func DecodeSlug(encodedSlug string) (*StreamInfo, error) {
	encodedSlug = strings.Replace(encodedSlug, "-", "+", -1)
	encodedSlug = strings.Replace(encodedSlug, "_", "/", -1)

	switch len(encodedSlug) % 4 {
	case 2:
		encodedSlug += "=="
	case 3:
		encodedSlug += "="
	}

	decodedData, err := base64.StdEncoding.DecodeString(encodedSlug)
	if err != nil {
		return nil, fmt.Errorf("error decoding Base64 data: %v", err)
	}

	var decompressedData []byte
	_, err = zstd.Decompress(decompressedData, decodedData)
	if err != nil {
		return nil, fmt.Errorf("error reading decompressed data: %v", err)
	}

	var result StreamInfo
	err = json.Unmarshal(decompressedData, &result)
	if err != nil {
		return nil, fmt.Errorf("error deserializing data: %v", err)
	}

	return &result, nil
}
