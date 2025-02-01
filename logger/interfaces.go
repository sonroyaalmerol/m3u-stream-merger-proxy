package logger

type Logger interface {
	Log(format string)
	Logf(format string, v ...any)

	Warn(format string)
	Warnf(format string, v ...any)

	Debug(format string)
	Debugf(format string, v ...any)

	Error(format string)
	Errorf(format string, v ...any)

	Fatal(format string)
	Fatalf(format string, v ...any)
}
