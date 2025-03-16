package logger

import (
	"fmt"
	"os"
	"regexp"

	"github.com/rs/zerolog"
)

type DefaultLogger struct {
	Logger
}

var Default = &DefaultLogger{}

var logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

func cleanString(text string) string {
	urlRegex := `[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[a-zA-Z0-9+%/.\-:_?&=#@+]+`
	re := regexp.MustCompile(urlRegex)

	safeString := re.ReplaceAllString(text, "[redacted url]")
	return safeString
}

func safeLogf(format string, v ...any) string {
	safeLogs := os.Getenv("SAFE_LOGS") == "true"
	safeString := fmt.Sprintf(format, v...)
	if safeLogs {
		return cleanString(safeString)
	}
	return safeString
}

func (*DefaultLogger) Log(format string) {
	logger.Info().Msg(safeLogf("%s", format))
}

func (*DefaultLogger) Logf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)
	logger.Info().Msg(safeLogf("%s", logString))
}

func (*DefaultLogger) Debug(format string) {
	debug := os.Getenv("DEBUG") == "true"

	if debug {
		logger.Debug().Msg(safeLogf("%s", format))
	}
}

func (*DefaultLogger) Debugf(format string, v ...any) {
	debug := os.Getenv("DEBUG") == "true"
	logString := fmt.Sprintf(format, v...)

	if debug {
		logger.Debug().Msg(safeLogf("%s", logString))
	}
}

func (*DefaultLogger) Error(format string) {
	logger.Error().Msg(safeLogf("%s", format))
}

func (*DefaultLogger) Errorf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)
	logger.Error().Msg(safeLogf("%s", logString))
}

func (*DefaultLogger) Warn(format string) {
	logger.Warn().Msg(safeLogf("%s", format))
}

func (*DefaultLogger) Warnf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)
	logger.Warn().Msg(safeLogf("%s", logString))
}

func (*DefaultLogger) Fatal(format string) {
	logger.Fatal().Msg(safeLogf("%s", format))
}

func (*DefaultLogger) Fatalf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)
	logger.Fatal().Msg(safeLogf("%s", logString))
}
