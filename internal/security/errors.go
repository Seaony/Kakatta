// Package security 提供密码、密钥、令牌和两步验证的计算，不管理存储或登录会话。
package security

import "errors"

var (
	ErrPasswordHash = errors.New("密码哈希格式或参数无效")
	ErrMasterKey    = errors.New("加密主密钥无效")
	ErrAAD          = errors.New("密文所属位置无效")
	ErrCiphertext   = errors.New("密文无法验证")
	ErrTOTPSecret   = errors.New("两步验证密钥无效")
	ErrTOTPInput    = errors.New("两步验证输入格式无效")
)
