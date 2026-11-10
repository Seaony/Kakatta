// Package api 保存服务端和网页共用的接口描述。
package api

import (
	"bytes"
	_ "embed"
)

//go:embed openapi.json
var document []byte

// JSON 返回副本，调用方不能改坏其他请求的接口描述。
func JSON() []byte { return bytes.Clone(document) }
