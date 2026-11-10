// Package web 提供构建好的网页文件。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var built embed.FS

func Assets() fs.FS {
	assets, _ := fs.Sub(built, "dist")
	return assets
}
