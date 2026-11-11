//go:build !kakatta_dev

package main

import (
	"context"
	"io"

	"github.com/Seaony/Kakatta/internal/config"
)

func runDevelopment(context.Context, []string, io.Writer, config.Lookup) (bool, error) {
	return false, nil
}
