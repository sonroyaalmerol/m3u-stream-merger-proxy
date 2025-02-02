package config

import "path/filepath"

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

func GetM3UCachePath() string {
	return filepath.Join(globalConfig.DataPath, "cache.m3u")
}

func GetStreamsDirPath() string {
	return filepath.Join(globalConfig.DataPath, "streams/")
}

func GetSourcesDirPath() string {
	return filepath.Join(globalConfig.TempPath, "sources/")
}
