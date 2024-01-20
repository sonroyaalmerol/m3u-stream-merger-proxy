package utils

import "encoding/base64"

func GetStreamUID(streamName string) string {
	return base64.URLEncoding.EncodeToString([]byte(streamName))
}

func GetStreamName(streamUID string) string {
	decoded, err := base64.URLEncoding.DecodeString(streamUID)
	if err != nil {
		return ""
	}
	return string(decoded)
}
