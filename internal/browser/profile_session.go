package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// 保留会话 Cookie，但不让 Chrome 自行恢复旧页面和请求。
func prepareSessionProfile(dir string) error {
	defaultDir := filepath.Join(dir, "Default")
	preferencesPath := filepath.Join(defaultDir, "Preferences")
	data, err := os.ReadFile(preferencesPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取浏览器偏好设置：%w", err)
	}
	preferences := map[string]json.RawMessage{}
	if err == nil {
		if err := json.Unmarshal(data, &preferences); err != nil || preferences == nil {
			return fmt.Errorf("浏览器偏好设置不是有效的对象，已停止启动")
		}
	}
	session := map[string]json.RawMessage{}
	if raw, exists := preferences["session"]; exists {
		if err := json.Unmarshal(raw, &session); err != nil || session == nil {
			return fmt.Errorf("浏览器偏好设置的 session 不是有效的对象，已停止启动")
		}
	}
	session["restore_on_startup"] = json.RawMessage("1")
	preferences["session"], err = json.Marshal(session)
	if err != nil {
		return fmt.Errorf("合并浏览器会话设置：%w", err)
	}
	data, err = json.Marshal(preferences)
	if err != nil {
		return fmt.Errorf("合并浏览器偏好设置：%w", err)
	}
	if err := os.MkdirAll(defaultDir, 0700); err != nil {
		return fmt.Errorf("创建浏览器偏好目录：%w", err)
	}
	for _, name := range []string{"Sessions", "Sessions_Encrypted"} {
		if err := os.RemoveAll(filepath.Join(defaultDir, name)); err != nil {
			return fmt.Errorf("清理浏览器页面恢复日志，已停止启动：%w", err)
		}
	}
	file, err := os.CreateTemp(defaultDir, ".Preferences-*")
	if err != nil {
		return fmt.Errorf("创建浏览器偏好临时文件：%w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("写入浏览器偏好临时文件：%w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("保存浏览器偏好临时文件：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭浏览器偏好临时文件：%w", err)
	}
	if err := os.Rename(file.Name(), preferencesPath); err != nil {
		return fmt.Errorf("替换浏览器偏好设置：%w", err)
	}
	return nil
}
