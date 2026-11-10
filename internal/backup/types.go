// Package backup 管理加密备份格式和恢复准备，运行环境切换交给服务入口。
package backup

import (
	"context"
	"time"

	"github.com/Seaony/Kakatta/internal/store"
)

const (
	Format         = "kakatta.backup"
	FormatVersion  = 1
	ManifestEntry  = "manifest.json"
	PayloadEntry   = "payload.bin"
	DatabaseEntry  = "kakatta.db"
	MasterKeyEntry = "secret.key"

	ChunkSize               = 1 << 20
	ManifestMaxBytes        = 64 << 10
	SaltBytes               = 16
	MasterKeyBytes          = 32
	NonceBytes              = 12
	TagBytes                = 16
	MaxChunkCount    uint64 = 1 << 32

	KDFName      = "argon2id"
	KDFTime      = 3
	KDFMemoryKiB = 64 * 1024
	KDFThreads   = 1

	PayloadMagic       = "KAKATTA\x00"
	AADDomain          = "kakatta.backup\x00"
	FrameFinal   uint8 = 1
)

type Manifest struct {
	Format        string `json:"format"`
	Version       int    `json:"version"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
	KDF           KDF    `json:"kdf"`
}

type KDF struct {
	Name      string `json:"name"`
	Salt      string `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint32 `json:"threads"`
}

type KeySource string

const (
	KeyDefaultFile  KeySource = "default_file"
	KeyEnvironment  KeySource = "environment"
	KeyExternalFile KeySource = "external_file"
)

// Source 的库与密钥必须来自同一运行代，密钥是实际使用值的私有副本。
type SnapshotSource struct {
	Store      *store.Store         `json:"-"`
	MasterKey  [MasterKeyBytes]byte `json:"-"`
	KeySource  KeySource            `json:"-"`
	Generation string               `json:"-"`
}

// Plan 只由完整验证过的候选产生，根入口负责保护记录合并和整套文件事务。
type RestorePlan struct {
	Manifest           Manifest             `json:"-"`
	DatabasePath       string               `json:"-"`
	MasterKey          [MasterKeyBytes]byte `json:"-"`
	KeySource          KeySource            `json:"-"`
	ExpectedGeneration string               `json:"-"`
}

type Runtime interface {
	// 回调期间保证同一代资源有效；不能在这里重读外部主密钥文件。
	WithSnapshot(context.Context, func(SnapshotSource) error) error
	// 拦住新工作并排空旧资源，成功后发布新运行代，失败保留完整旧数据。
	Install(context.Context, RestorePlan) error
}

type Dependencies struct {
	DataDir       string
	AppVersion    string
	SchemaVersion int
	Runtime       Runtime
	Now           func() time.Time
}

func (SnapshotSource) String() string   { return "备份源" }
func (SnapshotSource) GoString() string { return "备份源" }
func (RestorePlan) String() string      { return "恢复计划" }
func (RestorePlan) GoString() string    { return "恢复计划" }
