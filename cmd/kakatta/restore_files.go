package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Seaony/Kakatta/internal/backup"
)

var (
	errRestoreFilesState = errors.New("restore.invalid_state")
	errRestoreFilesIO    = errors.New("restore.storage_failure")
)

type restoreFilesError struct{ cause error }

func (e *restoreFilesError) Error() string    { return errRestoreFilesIO.Error() }
func (e *restoreFilesError) GoString() string { return e.Error() }
func (e *restoreFilesError) Unwrap() []error  { return []error{errRestoreFilesIO, e.cause} }

func restoreFileFailure(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, errRestoreFilesState} {
		if errors.Is(err, known) {
			return known
		}
	}
	return &restoreFilesError{cause: err}
}

type restoreResource struct {
	name, live       string
	directory, input bool
}

var restoreResources = [...]restoreResource{
	{"db", "kakatta.db", false, true}, {"wal", "kakatta.db-wal", false, false}, {"shm", "kakatta.db-shm", false, false},
	{"key", "secret.key", false, true}, {"browser", "browser", true, true}, {"images", "cache/images", true, true},
}

type restoreJournal struct {
	Version           int             `json:"version"`
	ID                string          `json:"id"`
	Phase             string          `json:"phase"`
	ReplaceDefaultKey bool            `json:"replace_default_key"`
	OldExisted        map[string]bool `json:"old_existed"`
}

type restoreFiles struct {
	dataDir, dir     string
	journal          restoreJournal
	mayHaveCommitted bool
	commitDurable    bool
	after            func(string) error
}

func (f *restoreFiles) String() string   { return "恢复文件事务" }
func (f *restoreFiles) GoString() string { return f.String() }

func restoreDir(path string, optional bool) error {
	info, err := os.Lstat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return restoreFileFailure(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errRestoreFilesState
	}
	return nil
}

func restoreExists(path string, directory bool) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, restoreFileFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return false, errRestoreFilesState
	}
	return true, nil
}

func (f *restoreFiles) step(label string, err error) error {
	if err != nil {
		return restoreFileFailure(err)
	}
	if f.after != nil {
		return restoreFileFailure(f.after(label))
	}
	return nil
}

func (f *restoreFiles) syncDir(path, label string) error {
	if err := restoreDir(path, false); err != nil {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return restoreFileFailure(err)
	}
	syncErr := f.step(label+".sync", dir.Sync())
	closeErr := f.step(label+".close", dir.Close())
	return errors.Join(syncErr, closeErr)
}

func (f *restoreFiles) syncFile(path, label string) error {
	file, err := os.Open(path)
	if err != nil {
		return restoreFileFailure(err)
	}
	syncErr := f.step(label+".sync", file.Sync())
	closeErr := f.step(label+".close", file.Close())
	return errors.Join(syncErr, closeErr)
}

func (f *restoreFiles) rename(from, to, label string) error {
	if err := f.step(label+".before_rename", nil); err != nil {
		return err
	}
	if err := f.step(label+".rename", os.Rename(from, to)); err != nil {
		return err
	}
	if err := f.syncDir(filepath.Dir(to), label+".destination_sync"); err != nil {
		return err
	}
	return f.syncDir(filepath.Dir(from), label+".source_sync")
}

func (f *restoreFiles) active(r restoreResource) bool {
	return r.name != "key" || f.journal.ReplaceDefaultKey
}
func (f *restoreFiles) live(r restoreResource) string { return filepath.Join(f.dataDir, r.live) }
func (f *restoreFiles) saved(area string, r restoreResource) string {
	return filepath.Join(f.dir, area, filepath.Base(r.live))
}

func stageRestoreFiles(ctx context.Context, dataDir string, plan backup.RestorePlan) (*restoreFiles, error) {
	return stageRestoreFilesWithHook(ctx, dataDir, plan, nil)
}

func stageRestoreFilesWithHook(ctx context.Context, dataDir string, plan backup.RestorePlan, after func(string) error) (_ *restoreFiles, err error) {
	defer clear(plan.MasterKey[:])
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if plan.KeySource != backup.KeyDefaultFile && plan.KeySource != backup.KeyEnvironment && plan.KeySource != backup.KeyExternalFile {
		return nil, errRestoreFilesState
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, restoreFileFailure(err)
	}
	if err := restoreDir(dataDir, false); err != nil {
		return nil, err
	}
	if err := restoreDir(filepath.Join(dataDir, "cache"), false); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "restore-state.json")); !errors.Is(err, os.ErrNotExist) {
		return nil, errRestoreFilesState
	}
	source, err := filepath.Abs(plan.DatabasePath)
	if err != nil {
		return nil, restoreFileFailure(err)
	}
	rel, err := filepath.Rel(dataDir, source)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errRestoreFilesState
	}
	for _, r := range restoreResources {
		if rel == r.live || r.directory && strings.HasPrefix(rel, r.live+string(filepath.Separator)) {
			return nil, errRestoreFilesState
		}
	}
	if rel == ".restore" || strings.HasPrefix(rel, ".restore"+string(filepath.Separator)) {
		return nil, errRestoreFilesState
	}
	if exists, err := restoreExists(source, false); err != nil || !exists {
		return nil, errRestoreFilesState
	}
	sourceInfo, err := os.Stat(source)
	if err != nil || sourceInfo.Size() == 0 {
		return nil, errRestoreFilesState
	}
	protected := []string{".server.lock", ".restore.lock", "restore-state.json"}
	for _, r := range restoreResources {
		protected = append(protected, r.live)
	}
	for _, name := range protected {
		info, statErr := os.Stat(filepath.Join(dataDir, name))
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, restoreFileFailure(statErr)
		}
		if statErr == nil && os.SameFile(sourceInfo, info) {
			return nil, errRestoreFilesState
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(source + suffix); !errors.Is(err, os.ErrNotExist) {
			return nil, errRestoreFilesState
		}
	}
	root := filepath.Join(dataDir, ".restore")
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, restoreFileFailure(err)
	}
	if err := restoreDir(root, false); err != nil {
		return nil, err
	}
	var random [16]byte
	rand.Read(random[:])
	f := &restoreFiles{dataDir: dataDir, after: after, journal: restoreJournal{Version: 1, ID: hex.EncodeToString(random[:]), Phase: "pending", ReplaceDefaultKey: plan.KeySource == backup.KeyDefaultFile, OldExisted: map[string]bool{}}}
	f.dir = filepath.Join(root, f.journal.ID)
	if err := os.Mkdir(f.dir, 0700); err != nil {
		return nil, restoreFileFailure(err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, restoreFileFailure(os.RemoveAll(f.dir)))
		}
	}()
	if err := f.step("stage.mkdir", nil); err != nil {
		return nil, err
	}
	for _, name := range []string{"new", "old", "new/browser", "new/images"} {
		if err := f.step("stage.mkdir."+name, os.Mkdir(filepath.Join(f.dir, name), 0700)); err != nil {
			return nil, err
		}
	}
	if f.journal.ReplaceDefaultKey {
		key, openErr := os.OpenFile(filepath.Join(f.dir, "new", backup.MasterKeyEntry), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return nil, restoreFileFailure(openErr)
		}
		_, writeErr := key.Write(plan.MasterKey[:])
		if writeErr == nil {
			writeErr = f.step("stage.key.write", nil)
		}
		if writeErr == nil {
			writeErr = f.step("stage.key.sync", key.Sync())
		}
		closeErr := f.step("stage.key.close", key.Close())
		if err := errors.Join(writeErr, closeErr); err != nil {
			return nil, restoreFileFailure(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	newDB := filepath.Join(f.dir, "new", backup.DatabaseEntry)
	if err := f.rename(source, newDB, "stage.db"); err != nil {
		return nil, err
	}
	if err := f.step("stage.db.chmod", os.Chmod(newDB, 0600)); err != nil {
		return nil, err
	}
	if err := f.syncFile(newDB, "stage.db.sync"); err != nil {
		return nil, err
	}
	for _, name := range []string{"new/browser", "new/images", "new", "old", ".", "..", "../.."} {
		if err := f.syncDir(filepath.Join(f.dir, name), "stage.sync."+name); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *restoreFiles) checkDirs(allowMissing bool) error {
	for _, path := range []string{f.dataDir, filepath.Join(f.dataDir, ".restore"), f.dir, filepath.Join(f.dir, "new"), filepath.Join(f.dir, "old")} {
		if err := restoreDir(path, allowMissing && path != f.dataDir); err != nil {
			return err
		}
	}
	return restoreDir(filepath.Join(f.dataDir, "cache"), false)
}

func (f *restoreFiles) writeJournal(phase string) (renamed bool, err error) {
	f.journal.Phase = phase
	raw, _ := json.Marshal(f.journal)
	file, err := os.CreateTemp(f.dataDir, ".restore-state-*")
	if err != nil {
		return false, restoreFileFailure(err)
	}
	defer os.Remove(file.Name())
	_, err = file.Write(raw)
	if err == nil {
		err = f.step(phase+".journal.write", nil)
	}
	if err == nil {
		err = f.step(phase+".journal.sync", file.Sync())
	}
	closeErr := f.step(phase+".journal.close", file.Close())
	if err = errors.Join(err, closeErr); err != nil {
		return false, restoreFileFailure(err)
	}
	if err = f.step(phase+".journal.before_rename", nil); err != nil {
		return false, err
	}
	if err = os.Rename(file.Name(), filepath.Join(f.dataDir, "restore-state.json")); err != nil {
		return false, restoreFileFailure(err)
	}
	if phase == "committed" {
		f.mayHaveCommitted = true
	}
	if err = f.step(phase+".journal.rename", nil); err != nil {
		return true, err
	}
	return true, f.syncDir(f.dataDir, phase+".journal.parent_sync")
}

func (f *restoreFiles) install(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.checkDirs(false); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(f.dataDir, "restore-state.json")); !errors.Is(err, os.ErrNotExist) {
		return errRestoreFilesState
	}
	for _, r := range restoreResources {
		f.journal.OldExisted[r.name] = false
		if !f.active(r) {
			continue
		}
		exists, err := restoreExists(f.live(r), r.directory)
		if err != nil {
			return err
		}
		f.journal.OldExisted[r.name] = exists
		if (r.name == "db" || r.name == "key") && !exists {
			return errRestoreFilesState
		}
		if saved, err := restoreExists(f.saved("old", r), r.directory); err != nil || saved {
			return errRestoreFilesState
		}
		if r.input {
			if exists, err := restoreExists(f.saved("new", r), r.directory); err != nil || !exists {
				return errRestoreFilesState
			}
		} else if exists, err := restoreExists(f.saved("new", r), r.directory); err != nil || exists {
			return errRestoreFilesState
		}
	}
	if _, err := f.writeJournal("pending"); err != nil {
		return err
	}
	for _, area := range []string{"old", "new"} {
		for _, r := range restoreResources {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !f.active(r) {
				continue
			}
			if area == "old" && f.journal.OldExisted[r.name] {
				if err := f.rename(f.live(r), f.saved("old", r), "old."+r.name); err != nil {
					return err
				}
			}
			if area == "new" && r.input {
				if err := f.rename(f.saved("new", r), f.live(r), "new."+r.name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (f *restoreFiles) commit() (bool, error) {
	if err := f.checkDirs(false); err != nil {
		return false, err
	}
	current, err := readRestoreJournal(f.dataDir)
	if err != nil || current.ID != f.journal.ID || current.Phase != "pending" {
		return false, errRestoreFilesState
	}
	for _, r := range restoreResources {
		if !f.active(r) {
			continue
		}
		old, err := restoreExists(f.saved("old", r), r.directory)
		if err != nil || old != f.journal.OldExisted[r.name] {
			return false, errRestoreFilesState
		}
		live, err := restoreExists(f.live(r), r.directory)
		if err != nil || r.input && !live {
			return false, errRestoreFilesState
		}
		if live {
			if r.directory {
				err = f.syncDir(f.live(r), "commit.sync."+r.name)
			} else {
				err = f.syncFile(f.live(r), "commit.sync."+r.name)
			}
			if err != nil {
				return false, err
			}
		}
	}
	for _, path := range []string{filepath.Join(f.dataDir, "cache"), f.dataDir} {
		if err := f.syncDir(path, "commit.parent_sync"); err != nil {
			return false, err
		}
	}
	may, err := f.writeJournal("committed")
	f.commitDurable = may && err == nil
	return may, err
}

func (f *restoreFiles) remove(path string, directory bool, label string) error {
	exists, err := restoreExists(path, directory)
	if err != nil || !exists {
		return err
	}
	if err := f.step(label+".remove", os.RemoveAll(path)); err != nil {
		return err
	}
	return f.syncDir(filepath.Dir(path), label+".parent_sync")
}

func (f *restoreFiles) rollback(ctx context.Context) error {
	if f.mayHaveCommitted {
		return errRestoreFilesState
	}
	j, err := readRestoreJournal(f.dataDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := f.checkDirs(true); err != nil {
			return err
		}
		old, err := os.ReadDir(filepath.Join(f.dir, "old"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return restoreFileFailure(err)
		}
		if len(old) != 0 {
			return errRestoreFilesState
		}
		return f.cleanStage(ctx)
	}
	if err != nil || j.ID != f.journal.ID || j.Phase != "pending" {
		return errRestoreFilesState
	}
	if err := f.checkDirs(false); err != nil {
		return err
	}
	f.journal = j
	for _, r := range restoreResources {
		if !f.active(r) {
			continue
		}
		old, err := restoreExists(f.saved("old", r), r.directory)
		if err != nil {
			return err
		}
		live, err := restoreExists(f.live(r), r.directory)
		if err != nil || !j.OldExisted[r.name] && old || j.OldExisted[r.name] && !old && !live {
			return errRestoreFilesState
		}
	}
	// 每项都按原存在性判断，不能删掉上次已还原的 WAL。
	for _, index := range []int{1, 2, 0, 3, 4, 5} {
		if err := ctx.Err(); err != nil {
			return err
		}
		r := restoreResources[index]
		if !f.active(r) {
			continue
		}
		old, _ := restoreExists(f.saved("old", r), r.directory)
		if old || !j.OldExisted[r.name] {
			if err := f.remove(f.live(r), r.directory, "rollback."+r.name); err != nil {
				return err
			}
		}
		if old {
			if err := f.rename(f.saved("old", r), f.live(r), "rollback."+r.name); err != nil {
				return err
			}
		}
	}
	if err := f.removeJournal("rollback"); err != nil {
		return err
	}
	return f.cleanStage(ctx)
}

func (f *restoreFiles) removeJournal(label string) error {
	if err := f.step(label+".journal.remove", os.Remove(filepath.Join(f.dataDir, "restore-state.json"))); err != nil {
		return err
	}
	return f.syncDir(f.dataDir, label+".journal.parent_sync")
}

func (f *restoreFiles) cleanStage(ctx context.Context) error {
	if err := f.checkDirs(true); err != nil {
		return err
	}
	for _, area := range []string{"old", "new"} {
		entries, err := os.ReadDir(filepath.Join(f.dir, area))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return restoreFileFailure(err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			var resource *restoreResource
			for _, r := range restoreResources {
				if entry.Name() == filepath.Base(r.live) && f.active(r) {
					copy := r
					resource = &copy
					break
				}
			}
			if resource == nil {
				return errRestoreFilesState
			}
			if err := f.remove(filepath.Join(f.dir, area, entry.Name()), resource.directory, "cleanup."+area+"."+resource.name); err != nil {
				return err
			}
		}
		if err := f.step("cleanup."+area+".remove", os.Remove(filepath.Join(f.dir, area))); err != nil {
			return err
		}
		if err := f.syncDir(f.dir, "cleanup."+area+".parent_sync"); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(f.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := f.step("cleanup.transaction.remove", os.Remove(f.dir)); err != nil {
		return err
	}
	return f.syncDir(filepath.Dir(f.dir), "cleanup.transaction.parent_sync")
}

func (f *restoreFiles) cleanupCommitted(ctx context.Context) error {
	if f.mayHaveCommitted && !f.commitDurable {
		return errRestoreFilesState
	}
	j, err := readRestoreJournal(f.dataDir)
	if err != nil || j.Phase != "committed" || j.ID != f.journal.ID {
		return errRestoreFilesState
	}
	f.journal = j
	if err := f.validateCommitted(); err != nil {
		return err
	}
	if err := f.cleanStage(ctx); err != nil {
		return err
	}
	return f.removeJournal("cleanup")
}

func (f *restoreFiles) validateCommitted() error {
	if err := f.checkDirs(true); err != nil {
		return err
	}
	for _, r := range restoreResources {
		if !f.active(r) || !r.input {
			continue
		}
		exists, err := restoreExists(f.live(r), r.directory)
		if err != nil {
			return err
		}
		if !exists {
			return errRestoreFilesState
		}
	}
	return nil
}

func exactRestoreObject(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errRestoreFilesState
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, errRestoreFilesState
		}
		key, ok := token.(string)
		allowed := false
		for _, name := range names {
			allowed = allowed || key == name
		}
		if !ok || !allowed || fields[key] != nil {
			return nil, errRestoreFilesState
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, errRestoreFilesState
		}
		fields[key] = value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(fields) != len(names) {
		return nil, errRestoreFilesState
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errRestoreFilesState
	}
	return fields, nil
}

func validRestoreID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == id
}

func readRestoreJournal(dataDir string) (restoreJournal, error) {
	path := filepath.Join(dataDir, "restore-state.json")
	exists, err := restoreExists(path, false)
	if err != nil {
		return restoreJournal{}, err
	}
	if !exists {
		return restoreJournal{}, os.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return restoreJournal{}, restoreFileFailure(err)
	}
	info, statErr := file.Stat()
	if statErr != nil || info.Mode().Perm() != 0600 {
		file.Close()
		return restoreJournal{}, errRestoreFilesState
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return restoreJournal{}, restoreFileFailure(err)
	}
	if len(raw) > 4096 {
		return restoreJournal{}, errRestoreFilesState
	}
	fields, err := exactRestoreObject(raw, "version", "id", "phase", "replace_default_key", "old_existed")
	if err != nil {
		return restoreJournal{}, err
	}
	if value := string(bytes.TrimSpace(fields["replace_default_key"])); value != "true" && value != "false" {
		return restoreJournal{}, errRestoreFilesState
	}
	old, err := exactRestoreObject(fields["old_existed"], "db", "wal", "shm", "key", "browser", "images")
	if err != nil {
		return restoreJournal{}, err
	}
	for _, raw := range old {
		value := string(bytes.TrimSpace(raw))
		if value != "true" && value != "false" {
			return restoreJournal{}, errRestoreFilesState
		}
	}
	var j restoreJournal
	if err := json.Unmarshal(raw, &j); err != nil || j.Version != 1 || !validRestoreID(j.ID) || j.Phase != "pending" && j.Phase != "committed" || !j.OldExisted["db"] || j.ReplaceDefaultKey != j.OldExisted["key"] {
		return restoreJournal{}, errRestoreFilesState
	}
	return j, nil
}

func recoverRestoreFiles(ctx context.Context, dataDir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := restoreDir(dataDir, false); err != nil {
		return false, err
	}
	if err := restoreDir(filepath.Join(dataDir, ".restore"), true); err != nil {
		return false, err
	}
	j, err := readRestoreJournal(dataDir)
	if err == nil {
		f := &restoreFiles{dataDir: dataDir, dir: filepath.Join(dataDir, ".restore", j.ID), journal: j}
		if j.Phase == "pending" {
			return false, f.rollback(ctx)
		}
		if err := f.validateCommitted(); err != nil {
			return false, err
		}
		if err := f.cleanupCommitted(ctx); err != nil {
			if errors.Is(err, errRestoreFilesState) {
				return false, err
			}
			return true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, ".restore"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, restoreFileFailure(err)
	}
	for _, entry := range entries {
		if !validRestoreID(entry.Name()) {
			continue
		}
		f := &restoreFiles{dataDir: dataDir, dir: filepath.Join(dataDir, ".restore", entry.Name()), journal: restoreJournal{ReplaceDefaultKey: true}}
		if err := f.checkDirs(true); err != nil {
			return false, err
		}
		old, err := os.ReadDir(filepath.Join(f.dir, "old"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, restoreFileFailure(err)
		}
		if len(old) != 0 {
			return false, errRestoreFilesState
		}
		if err := f.cleanStage(ctx); err != nil {
			return false, err
		}
	}
	return false, nil
}
