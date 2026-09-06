package sourceproc

import (
	"fmt"
	"os"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
)

func GetStreamBySlug(slug string) (*StreamInfo, error) {
	var err error
	streamInfo, err := ParseStreamInfoBySlug(slug)
	if err != nil {
		return &StreamInfo{}, fmt.Errorf("error parsing stream info: %v", err)
	}

	return streamInfo, nil
}

func ClearProcessedM3Us() {
	err := os.RemoveAll(config.GetProcessedDirPath())
	if err != nil {
		logger.Default.Error(err.Error())
	}
}
