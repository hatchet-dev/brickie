// Package logging provides a logger for the Brickie project
package logging

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rs/zerolog"
)

var hatchet zerolog.Logger

func Setup(format string) error {
	switch format {
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		hatchet = zerolog.New(os.Stdout).With().Timestamp().Str("component", "hatchet").Logger()
	case "text":
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
		hatchet = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Str("component", "hatchet").Logger()
	default:
		return fmt.Errorf("--log-format %q: want json or text", format)
	}
	hatchet = hatchet.Level(zerolog.WarnLevel)
	return nil
}

func Hatchet() *zerolog.Logger {
	return &hatchet
}
