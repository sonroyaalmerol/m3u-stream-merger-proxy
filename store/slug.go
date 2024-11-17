package store

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"m3u-stream-merger/utils"
	"os"
	"strings"

	"github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
)

var debug = os.Getenv("DEBUG") == "true"

func EncodeSlug(stream StreamInfo) string {
	jsonData, err := json.Marshal(stream)
	if err != nil {
		if debug {
			utils.SafeLogf("[DEBUG] Error json marshal for slug: %v\n", err)
		}
		return ""
	}

	var compressedData bytes.Buffer
	writer, err := zstd.NewWriter(&compressedData)
	if err != nil {
		if debug {
			utils.SafeLogf("[DEBUG] Error zstd compression for slug: %v\n", err)
		}
		return ""
	}

	_, err = writer.Write(jsonData)
	if err != nil {
		if debug {
			utils.SafeLogf("[DEBUG] Error zstd compression for slug: %v\n", err)
		}
		return ""
	}
	writer.Close()

	encodedData := base64.StdEncoding.EncodeToString(compressedData.Bytes())

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

	reader, err := zstd.NewReader(bytes.NewReader(decodedData))
	if err != nil {
		return nil, fmt.Errorf("error reading decompressed data: %v", err)
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
