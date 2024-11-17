package store

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

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
	decodedData, err := base64.StdEncoding.DecodeString(encodedSlug)
	if err != nil {
		return nil, fmt.Errorf("error decoding Base64 data: %v", err)
	}

	strData := string(decodedData)
	strData = strings.Replace(strData, "-", "+", -1)
	strData = strings.Replace(strData, "_", "/", -1)

	switch len(strData) % 4 {
	case 0:
	case 1:
	case 2:
		strData += "=="
	case 3:
		strData += "="
	}

	reader, err := zstd.NewReader(bytes.NewReader([]byte(strData)))
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
