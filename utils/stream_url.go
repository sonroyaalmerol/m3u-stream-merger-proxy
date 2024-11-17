package utils

import (
	"net/url"
	"path"
	"strings"
)

func GetFileExtensionFromUrl(rawUrl string) (string, error) {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return "", err
	}
	return path.Ext(u.Path), nil
}

func GetSubPathFromUrl(rawUrl string) (string, error) {
	parsedURL, err := url.Parse(rawUrl)
	if err != nil {
		return "", err
	}

	pathSegments := strings.Split(parsedURL.Path, "/")

	if len(pathSegments) <= 1 {
		return "stream", nil
	}

	basePath := strings.Join(pathSegments[1:len(pathSegments)-1], "/")
	return basePath, nil
}
