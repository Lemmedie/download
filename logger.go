package main

import (
	"log/slog"
	"os"
)

var logger *slog.Logger

func init() {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{AddSource: false})
	logger = slog.New(handler)
}
