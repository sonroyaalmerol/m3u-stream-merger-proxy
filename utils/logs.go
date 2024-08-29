package utils

import (
	"fmt"
	"log"
	"os"
	"regexp"
)

func cleanString(text string) string {
	urlRegex := `[a-zA-Z][a-zA-Z0-9+.-]*:\/\/[a-zA-Z0-9+%/.-]+`
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

func SafeLogf(format string, v ...any) {
	log.Print(safeLogf(format, v...))
}

func SafeLog(format string) {
	log.Print(safeLog(format))
}

func SafeLogln(format string) {
	log.Println(safeLog(format))
}

func SafeLogFatal(format string) {
	log.Fatal(safeLog(format))
}

func SafeLogFatalf(format string, v ...any) {
	log.Fatal(safeLogf(format, v...))
}
