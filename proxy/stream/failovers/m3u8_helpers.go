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

var (
	encoderPool sync.Pool
	decoderPool sync.Pool
)

func init() {
	encoderPool = sync.Pool{
		New: func() interface{} {
			encoder, err := zstd.NewWriter(nil)
			if err != nil {
				logger.Default.Debugf("Error creating zstd encoder: %v", err)
				return nil
			}
			return encoder
		},
	}

	decoderPool = sync.Pool{
		New: func() interface{} {
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
	encoder.Close()

	encodedData := base64.RawURLEncoding.EncodeToString(compressedData.Bytes())
	return encodedData
}

func decodeSlug(encodedSlug string) (*M3U8Segment, error) {
	decodedData, err := base64.RawURLEncoding.DecodeString(encodedSlug)
	if err != nil {
		return nil, fmt.Errorf("error decoding Base64 data: %v", err)
	}

	decoder := decoderPool.Get().(*zstd.Decoder)
	defer decoderPool.Put(decoder)
	_ = decoder.Reset(bytes.NewReader(decodedData))

	decompressedData, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("error reading decompressed data: %v", err)
	}

	var result M3U8Segment
	if err := json.Unmarshal(decompressedData, &result); err != nil {
		return nil, fmt.Errorf("error deserializing data: %v", err)
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
