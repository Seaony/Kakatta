package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Bundle 只接受调用方明确指定的失败响应，普通事件没有正文入口。
func (r *Recorder) Bundle(kind string, page Page, response []byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.err != nil {
		return "", errors.Join(errors.New("诊断记录不可写"), r.err)
	}
	root := filepath.Join(r.dir, "bundles")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", r.failed(err)
	}
	dir, err := os.MkdirTemp(root, "bundle-"+r.now().In(r.loc).Format("20060102-150405")+"-")
	if err != nil {
		return "", r.failed(err)
	}
	good := false
	defer func() {
		if !good {
			os.RemoveAll(dir)
		}
	}()
	metadata, err := json.Marshal(map[string]any{
		"version": 1, "kind": kind, "at": r.now().In(r.loc), "sequence": r.sequence,
		"url": SafeURL(page.URL), "title": page.Title, "capture_errors": page.Errors, "login_page": page.LoginPage,
	})
	if err != nil {
		return "", r.failed(err)
	}
	events, _ := json.Marshal(r.events)
	files := map[string][]byte{"page.json": redactJSON(metadata), "events.json": events}
	if len(page.Screenshot) > 0 {
		files["screenshot.jpg"] = page.Screenshot
	}
	if page.HTML != "" {
		files["page.html"] = []byte(redactHTML(page.HTML))
	}
	if response != nil {
		files["response.json"] = []byte(RedactText(string(response)))
	}
	var total int
	for _, content := range files {
		total += len(content)
	}
	if total > maxPackBytes {
		return "", r.failed(fmt.Errorf("诊断包超过 %d 字节上限", maxPackBytes))
	}
	for name, content := range files {
		if err := atomicFile(filepath.Join(dir, name), content); err != nil {
			return "", r.failed(err)
		}
	}
	if err := pruneDirectories(root, maxBundles, maxPackTotal); err != nil {
		return "", r.failed(err)
	}
	good = true
	return dir, nil
}

// Summary 每次结束单独留档，避免下一次运行覆盖上一份结论。
func (r *Recorder) Summary(summary any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.err != nil {
		return errors.Join(errors.New("诊断记录不可写"), r.err)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return r.failed(err)
	}
	data = redactJSON(data)
	if len(data) > maxEventSize {
		return r.failed(errors.New("运行摘要超过大小上限"))
	}
	root := filepath.Join(r.dir, "summaries")
	if err := os.MkdirAll(root, 0700); err != nil {
		return r.failed(err)
	}
	dir, err := os.MkdirTemp(root, "summary-"+r.now().In(r.loc).Format("20060102-150405")+"-")
	if err != nil {
		return r.failed(err)
	}
	if err := atomicFile(filepath.Join(dir, "summary.json"), data); err != nil {
		os.RemoveAll(dir)
		return r.failed(err)
	}
	return r.failed(pruneDirectories(root, 30, 2<<20))
}

func pruneDirectories(root string, maxCount int, maxBytes int64) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type directory struct {
		path string
		size int64
		at   time.Time
	}
	directories := []directory{}
	var total int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := directory{path: filepath.Join(root, entry.Name()), at: info.ModTime()}
		if err := filepath.WalkDir(item.path, func(path string, child os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !child.IsDir() {
				info, err := child.Info()
				if err != nil {
					return err
				}
				item.size += info.Size()
			}
			return nil
		}); err != nil {
			return err
		}
		total += item.size
		directories = append(directories, item)
	}
	sort.Slice(directories, func(i, j int) bool {
		if directories[i].at.Equal(directories[j].at) {
			return directories[i].path < directories[j].path
		}
		return directories[i].at.Before(directories[j].at)
	})
	for index, item := range directories {
		if len(directories)-index <= maxCount && total <= maxBytes {
			break
		}
		if err := os.RemoveAll(item.path); err != nil {
			return err
		}
		total -= item.size
	}
	return nil
}
