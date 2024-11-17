package utils

import "fmt"

func GetM3UFilePathByIndex(m3uIndex int) string {
	m3uFile := fmt.Sprintf("/tmp/m3u-proxy/sources/%d.m3u", m3uIndex+1)

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
