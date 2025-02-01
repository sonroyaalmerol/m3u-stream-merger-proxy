package logger

import (
	"fmt"
	"log"
	"os"
	"regexp"
)

type DefaultLogger struct {
	Logger
}

var Default = &DefaultLogger{}

func cleanString(text string) string {
	urlRegex := `[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[a-zA-Z0-9+%/.\-:_?&=#@+]+`
	re := regexp.MustCompile(urlRegex)

	safeString := re.ReplaceAllString(text, "[redacted url]")
	return safeString
}

func safeLog(format string) string {
	safeLogs := os.Getenv("SAFE_LOGS") == "true"
	safeString := format
	if safeLogs {
		return cleanString(safeString)
	}
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
	log.Println(safeLogf("[INFO] %s", format))
}

func (*DefaultLogger) Logf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)

	log.Println(safeLogf("[INFO] %s", logString))
}

func (*DefaultLogger) Debug(format string) {
	debug := os.Getenv("DEBUG") == "true"

	if debug {
		log.Println(safeLogf("[DEBUG] %s", format))
	}
}

func (*DefaultLogger) Debugf(format string, v ...any) {
	debug := os.Getenv("DEBUG") == "true"
	logString := fmt.Sprintf(format, v...)

	if debug {
		log.Println(safeLogf("[DEBUG] %s", logString))
	}
}

func (*DefaultLogger) Error(format string) {
	log.Println(safeLogf("[ERROR] %s", format))
}

func (*DefaultLogger) Errorf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)

	log.Println(safeLogf("[ERROR] %s", logString))
}

func (*DefaultLogger) Warn(format string) {
	log.Println(safeLogf("[WARN] %s", format))
}

func (*DefaultLogger) Warnf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)

	log.Println(safeLogf("[WARN] %s", logString))
}

func (*DefaultLogger) Fatal(format string) {
	log.Fatal(safeLogf("[FATAL] %s", format))
}

func (*DefaultLogger) Fatalf(format string, v ...any) {
	logString := fmt.Sprintf(format, v...)

	log.Fatal(safeLogf("[FATAL] %s", logString))
}
