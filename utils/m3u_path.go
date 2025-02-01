package utils

import (
	"fmt"
	"m3u-stream-merger/config"
	"path/filepath"
)

func GetM3UFilePathByIndex(m3uIndex string) string {
	m3uFile := filepath.Join(config.GetSourcesDirPath(), fmt.Sprintf("%s.m3u", m3uIndex))

	return m3uFile
}

func GetAllM3UFilePaths() []string {
	paths := []string{}
	m3uIndexes := GetM3UIndexes()
	for _, idx := range m3uIndexes {
		paths = append(paths, GetM3UFilePathByIndex(idx))
	}

	return paths
}
