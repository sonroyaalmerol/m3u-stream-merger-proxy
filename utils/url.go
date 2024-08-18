package utils

import "encoding/base64"

func GetStreamUrl(slug string) string {
	return base64.URLEncoding.EncodeToString([]byte(slug))
}

func GetStreamSlugFromUrl(streamUID string) string {
	decoded, err := base64.URLEncoding.DecodeString(streamUID)
	if err != nil {
		return ""
	}
	return string(decoded)
}
