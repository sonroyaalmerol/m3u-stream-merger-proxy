package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	DataPath string
	TempPath string
}

var globalConfig = &Config{
	DataPath: "/m3u-proxy/data/",
	TempPath: "/tmp/m3u-proxy/",
}

func GetConfig() *Config {
	return globalConfig
}

func SetConfig(c *Config) {
	globalConfig = c
}

func GetProcessedDirPath() string {
	return filepath.Join(globalConfig.DataPath, "processed/")
}

func GetLatestProcessedM3UPath() (string, error) {
	dir := GetProcessedDirPath()
	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	var validFiles []os.DirEntry
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".tmp") {
			continue
		}
		validFiles = append(validFiles, file)
	}

	if len(files) == 0 {
		return "", fmt.Errorf("no files found in directory")
	}

	return validFiles[len(files)-1].Name(), nil
}

func GetNewM3UPath() string {
	now := time.Now()

	filename := now.Format("20060102150405")
	return filepath.Join(GetProcessedDirPath(), filename+".m3u")
}

func ClearOldProcessedM3U(latestFilename string) error {
	dir := GetProcessedDirPath()
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		filePath := filepath.Join(dir, file.Name())

		if filePath == latestFilename {
			continue
		}

		err := os.Remove(filePath)
		if err != nil {
			return fmt.Errorf("failed to delete file %s: %w", filePath, err)
		}
	}

	return nil
}

func GetStreamsDirPath() string {
	return filepath.Join(globalConfig.DataPath, "streams/")
}

func GetSourcesDirPath() string {
	return filepath.Join(globalConfig.TempPath, "sources/")
}

func GetSortDirPath() string {
	return filepath.Join(globalConfig.TempPath, "sorter/")
}
