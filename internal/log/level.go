package log

import "github.com/moonfruit/sing2seq/clef"

type Level = clef.Level

const (
	LevelTrace = clef.LevelTrace
	LevelDebug = clef.LevelDebug
	LevelInfo  = clef.LevelInfo
	LevelWarn  = clef.LevelWarn
	LevelError = clef.LevelError
	LevelFatal = clef.LevelFatal
)

var (
	ParseLevel   = clef.ParseLevel
	FromCLEFName = clef.FromCLEFName
)
