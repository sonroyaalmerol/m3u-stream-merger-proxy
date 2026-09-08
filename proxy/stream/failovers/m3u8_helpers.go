package failovers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/utils"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const maxSegmentSlugBytes = 64 << 10

var (
	encoderPool sync.Pool
	decoderPool sync.Pool
)

func init() {
	encoderPool = sync.Pool{
		New: func() any {
			encoder, err := zstd.NewWriter(nil)
			if err != nil {
				logger.Default.Debugf("Error creating zstd encoder: %v", err)
				return nil
			}
			return encoder
		},
	}

	decoderPool = sync.Pool{
		New: func() any {
			decoder, err := zstd.NewReader(nil)
			if err != nil {
				logger.Default.Debugf("Error creating zstd decoder: %v", err)
				return nil
			}
			return decoder
		},
	}
}

func encodeSlug(stream *M3U8Segment) string {
	jsonData, err := json.Marshal(stream)
	if err != nil {
		logger.Default.Debugf("Error json marshal for slug: %v", err)
		return ""
	}

	encoder := encoderPool.Get().(*zstd.Encoder)
	defer encoderPool.Put(encoder)
	encoder.Reset(nil)

	var compressedData bytes.Buffer
	encoder.Reset(&compressedData)

	if _, err := encoder.Write(jsonData); err != nil {
		logger.Default.Debugf("Error zstd compression for slug: %v", err)
		return ""
	}
	if err := encoder.Close(); err != nil {
		logger.Default.Debugf("Error closing zstd encoder: %v", err)
		return ""
	}

	encodedData := base64.RawURLEncoding.EncodeToString(compressedData.Bytes())
	return encodedData
}

func decodeSlug(encodedSlug string) (*M3U8Segment, error) {
	decodedData, err := base64.RawURLEncoding.DecodeString(encodedSlug)
	if err != nil {
		return nil, fmt.Errorf("decode base64 data: %w", err)
	}

	decoder := decoderPool.Get().(*zstd.Decoder)
	defer decoderPool.Put(decoder)
	if err := decoder.Reset(bytes.NewReader(decodedData)); err != nil {
		return nil, fmt.Errorf("reset zstd decoder: %w", err)
	}

	decompressedData, err := io.ReadAll(io.LimitReader(decoder, maxSegmentSlugBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read decompressed data: %w", err)
	}
	if len(decompressedData) > maxSegmentSlugBytes {
		return nil, fmt.Errorf("decompressed segment slug exceeds %d bytes", maxSegmentSlugBytes)
	}

	var result M3U8Segment
	if err := json.Unmarshal(decompressedData, &result); err != nil {
		return nil, fmt.Errorf("deserialize data: %w", err)
	}

	return &result, nil
}

func generateSegmentURL(stream *M3U8Segment) string {
	baseUrl := utils.DetermineBaseURL(nil)

	extension, err := utils.GetFileExtensionFromUrl(stream.URL)
	if err != nil {
		extension = ""
	}

	finalUrl := fmt.Sprintf("%s/segment/%s", baseUrl, encodeSlug(stream))

	return finalUrl + extension
}

func ParseSegmentId(id string) (*M3U8Segment, error) {
	initInfo, err := decodeSlug(id)
	if err != nil {
		return nil, err
	}

	return initInfo, nil
}
