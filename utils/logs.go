package utils

import (
	"fmt"
	"log"
	"os"
	"regexp"
)

func safeLog(format string, v ...any) string {
	safeLogs := os.Getenv("SAFE_LOGS") == "true"
	safeString := ""
	if len(v) == 0 {
		safeString = format
	} else {
		safeString = fmt.Sprintf(format, v...)
	}
	if safeLogs {
		urlRegex := `(https?|file):\/\/[^\s/$.?#].[^\s]*`
		re := regexp.MustCompile(urlRegex)

		safeString = re.ReplaceAllString(safeString, "[redacted url]")
	}
	return safeString
}

func SafeLog(format string, v ...any) {
	log.Print(safeLog(format, v...))
}

func SafeLogln(format string) {
	log.Println(safeLog(format))
}

func SafeLogFatal(format string, v ...any) {
	log.Fatalf(safeLog(format, v...))
}
