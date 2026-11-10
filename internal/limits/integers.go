// Package limits 定义各业务入口共用的数值边界。
package limits

const (
	MaxSafeInteger int64 = 9007199254740991
	MinSafeInteger int64 = -MaxSafeInteger
)
