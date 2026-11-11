package auth

import (
	"crypto/rand"
	"fmt"
	"os"
)

const initialTokenFilename = "初始化令牌.txt"

func initialTokenFileError(action string) error {
	return fmt.Errorf("初始化令牌文件%s失败，请检查数据目录权限和文件类型：%w", action, ErrStorage)
}

func initialTokenRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, initialTokenFileError("打开")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, initialTokenFileError("打开")
	}
	return root, nil
}

func (s *Service) writeInitialTokenFile(token string) error {
	if s.dataDir == "" {
		return nil
	}
	root, err := initialTokenRoot(s.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(initialTokenFilename); err != nil && !os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		return initialTokenFileError("写入")
	}
	name := ".initial-token-" + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return initialTokenFileError("写入")
	}
	defer root.Remove(name)
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return initialTokenFileError("写入")
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		return initialTokenFileError("写入")
	}
	if err := file.Sync(); err != nil {
		return initialTokenFileError("写入")
	}
	if err := file.Close(); err != nil {
		return initialTokenFileError("写入")
	}
	if err := root.Rename(name, initialTokenFilename); err != nil {
		return initialTokenFileError("替换")
	}
	return nil
}

func (s *Service) removeInitialTokenFile() error {
	if s.dataDir == "" {
		return nil
	}
	root, err := initialTokenRoot(s.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(initialTokenFilename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return initialTokenFileError("清理")
	}
	if err := root.Remove(initialTokenFilename); err != nil && !os.IsNotExist(err) {
		return initialTokenFileError("清理")
	}
	return nil
}
