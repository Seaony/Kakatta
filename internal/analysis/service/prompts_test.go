package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func promptDefinitions(t *testing.T) []prompts.Definition {
	t.Helper()
	definitions, err := prompts.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	return definitions
}

func promptError(t *testing.T, err error, kind service.ErrorKind, code string) *service.ServiceError {
	t.Helper()
	var problem *service.ServiceError
	if !errors.As(err, &problem) || problem.Kind != kind || problem.Reason.Code != code {
		t.Fatalf("unexpected prompt error: %v", err)
	}
	return problem
}

func promptEventCount(f *modelFixture) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func TestPromptReadsReturnFreshAssetsWithoutWritingClockOrDefaults(t *testing.T) {
	f := newModelFixture(t)
	clock := modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey)
	f.now.Add(int64(24 * time.Hour / time.Millisecond))
	for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
		f.exec(t, `CREATE TRIGGER forbid_prompt_`+operation+` BEFORE `+operation+` ON prompts BEGIN SELECT RAISE(ABORT,'read wrote prompts'); END`)
	}
	f.exec(t, `CREATE TRIGGER forbid_prompt_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'read wrote clock'); END`)
	views, err := f.s.ListPrompts(t.Context())
	definitions := promptDefinitions(t)
	if err != nil || len(views) != 4 {
		t.Fatal(views, err)
	}
	for i, view := range views {
		d := definitions[i]
		if view.Kind != d.Kind || view.Content != d.Default || view.Default != d.Default || view.Customized || view.UpdatedAt != nil || view.AssetVersion != prompts.AssetVersion || !reflect.DeepEqual(view.RequiredKeys, d.RequiredKeys) || !reflect.DeepEqual(view.Schema, d.Schema) {
			t.Fatalf("default view differs from assets: %+v", view)
		}
	}
	views[0].RequiredKeys[0], views[0].Schema.JSON[0] = "changed", '!'
	snapshot, err := f.s.PromptSnapshot(t.Context())
	if err != nil || snapshot.Contents[client.Analysis] != definitions[0].Default {
		t.Fatal(snapshot, err)
	}
	snapshot.Contents[client.Analysis] = "changed"
	again, err := f.s.ListPrompts(t.Context())
	if err != nil || !reflect.DeepEqual(again[0].RequiredKeys, definitions[0].RequiredKeys) || !reflect.DeepEqual(again[0].Schema, definitions[0].Schema) || again[0].Content != definitions[0].Default {
		t.Fatal("returned metadata changed later reads", again, err)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM prompts`) != 0 || modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey) != clock || promptEventCount(f) != 0 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
		t.Fatal("prompt reads changed durable state or emitted an event")
	}
}

func TestPromptSaveRestoreAndDefaultEqualityHaveExactChangeEvents(t *testing.T) {
	f := newModelFixture(t)
	type event struct {
		kind string
		data json.RawMessage
	}
	var events []event
	f.s.publish = func(kind string, reason *locales.Message, raw json.RawMessage) error {
		if reason != nil {
			t.Fatal("prompt change must not attach template text as a reason")
		}
		var data map[string]string
		if json.Unmarshal(raw, &data) != nil || len(data) != 1 || data["kind"] == "" {
			t.Fatalf("invalid prompt event: %s", raw)
		}
		// 只有提交后才能从另一个数据库调用读到结果。
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		var count int
		if err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM prompts WHERE key=?`, data["kind"]).Scan(&count); err != nil {
			t.Fatal("prompt event ran before commit", err)
		}
		events = append(events, event{kind, append(json.RawMessage{}, raw...)})
		return nil
	}
	for _, definition := range promptDefinitions(t) {
		content := " \n" + definition.Default + "\t自定义末尾 "
		view, err := f.s.SavePrompt(t.Context(), definition.Kind, content)
		if err != nil || view.Content != content || !view.Customized || view.UpdatedAt == nil || *view.UpdatedAt != f.now.Load() || view.Default != definition.Default {
			t.Fatal(view, err)
		}
		at := *view.UpdatedAt
		count := len(events)
		f.now.Add(1000)
		view, err = f.s.SavePrompt(t.Context(), definition.Kind, content)
		if err != nil || *view.UpdatedAt != at || len(events) != count {
			t.Fatal("saving identical content changed state", view, err)
		}
		snapshot, err := f.s.PromptSnapshot(t.Context())
		if err != nil || snapshot.Contents[definition.Kind] != content {
			t.Fatal(snapshot, err)
		}
		view, err = f.s.RestorePrompt(t.Context(), definition.Kind)
		if err != nil || view.Content != definition.Default || view.Customized || view.UpdatedAt != nil || len(events) != count+1 {
			t.Fatal(view, err)
		}
		view, err = f.s.RestorePrompt(t.Context(), definition.Kind)
		if err != nil || view.Customized || len(events) != count+1 {
			t.Fatal("restoring a default emitted a false change", view, err)
		}
		if _, err := f.s.SavePrompt(t.Context(), definition.Kind, content); err != nil {
			t.Fatal(err)
		}
		view, err = f.s.SavePrompt(t.Context(), definition.Kind, definition.Default)
		if err != nil || view.Customized || view.UpdatedAt != nil || modelScalar[int](t, f, `SELECT count(*) FROM prompts WHERE key=?`, definition.Kind) != 0 {
			t.Fatal("saving exact default did not delete override", view, err)
		}
	}
	if len(events) != 16 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 || len(f.notified) != 0 {
		t.Fatal("prompt changes triggered model or system work", len(events))
	}
	for _, event := range events {
		if event.kind != "ai.prompts.changed" || strings.Contains(string(event.data), "自定义") {
			t.Fatal("prompt event contained the wrong producer or template", event)
		}
	}
}

func TestPromptValidationKeepsOtherTemplatesAndReportsRequiredKeys(t *testing.T) {
	f := newModelFixture(t)
	for _, d := range promptDefinitions(t) {
		for _, key := range d.RequiredKeys {
			content := strings.ReplaceAll(d.Default, `"`+key+`"`, key)
			_, err := f.s.SavePrompt(t.Context(), d.Kind, content)
			problem := promptError(t, err, service.ErrorInvalid, "analysis.prompts.missing_keys")
			if problem.Reason.Args["kind"] != string(d.Kind) || !strings.Contains(problem.Reason.Args["keys"].(string), `"`+key+`"`) || len(problem.Fields) != 1 || problem.Fields[0].Key != "content" {
				t.Fatal(problem)
			}
		}
	}
	for _, restore := range []bool{false, true} {
		var err error
		if restore {
			_, err = f.s.RestorePrompt(t.Context(), client.Kind("unknown"))
		} else {
			_, err = f.s.SavePrompt(t.Context(), client.Kind("unknown"), "private template")
		}
		problem := promptError(t, err, service.ErrorInvalid, "analysis.prompts.invalid_input")
		if problem.Reason.Args["field"] != "kind" || problem.Fields[0].Key != "kind" {
			t.Fatal(problem)
		}
	}
	_, err := f.s.SavePrompt(t.Context(), client.Market, string([]byte{0xff}))
	promptError(t, err, service.ErrorInvalid, "analysis.prompts.invalid_input")
	if modelScalar[int](t, f, `SELECT count(*) FROM prompts`) != 0 || promptEventCount(f) != 0 {
		t.Fatal("validation failure wrote partial state")
	}
	// 模板是原文，不要求整段是 JSON，也不额外限制行情模板内容。
	content := `保留 "checks" "adjust" "summary"，这里不是有效 JSON：{`
	if view, err := f.s.SavePrompt(t.Context(), client.Analysis, content); err != nil || view.Content != content {
		t.Fatal(view, err)
	}
	for _, content := range []string{"", " \n\t"} {
		if view, err := f.s.SavePrompt(t.Context(), client.Market, content); err != nil || view.Content != content || !view.Customized {
			t.Fatal("market template gained an undocumented constraint", view, err)
		}
	}
}

func TestPromptCorruptStoredContentFailsClosedAndCanBeRestored(t *testing.T) {
	f := newModelFixture(t)
	f.exec(t, `INSERT INTO prompts(key,content) VALUES('analysis','{"checks":')`)
	_, err := f.s.ListPrompts(t.Context())
	promptError(t, err, service.ErrorUnavailable, "service.storage_failure")
	_, err = f.s.PromptSnapshot(t.Context())
	promptError(t, err, service.ErrorUnavailable, "service.storage_failure")
	if modelScalar[string](t, f, `SELECT content FROM prompts WHERE key='analysis'`) != `{"checks":` || promptEventCount(f) != 0 {
		t.Fatal("reading corrupt content silently reset it")
	}
	if view, err := f.s.RestorePrompt(t.Context(), client.Analysis); err != nil || view.Customized {
		t.Fatal("restore could not repair the corrupt override", view, err)
	}
	f.exec(t, `PRAGMA ignore_check_constraints=ON`)
	f.exec(t, `INSERT INTO prompts(key,content) VALUES('unknown','private broken content')`)
	f.exec(t, `PRAGMA ignore_check_constraints=OFF`)
	count := promptEventCount(f)
	_, err = f.s.SavePrompt(t.Context(), client.Market, "new candidate")
	problem := promptError(t, err, service.ErrorUnavailable, "service.storage_failure")
	encoded, _ := json.Marshal(problem)
	if strings.Contains(string(encoded), "private broken content") || modelScalar[int](t, f, `SELECT count(*) FROM prompts WHERE key='market'`) != 0 || promptEventCount(f) != count {
		t.Fatal("unknown stored kind leaked or committed a partial candidate", problem)
	}
}

func TestPromptWriteAndCommitFailureRollBackWithoutEvents(t *testing.T) {
	f := newModelFixture(t)
	content := promptDefinitions(t)[0].Default + "\ncustom"
	if _, err := f.s.SavePrompt(t.Context(), client.Analysis, content); err != nil {
		t.Fatal(err)
	}
	count := promptEventCount(f)
	for _, operation := range []string{"UPDATE", "DELETE"} {
		f.exec(t, `CREATE TRIGGER reject_prompt BEFORE `+operation+` ON prompts BEGIN SELECT RAISE(ABORT,'private failure'); END`)
		var err error
		if operation == "UPDATE" {
			_, err = f.s.SavePrompt(t.Context(), client.Analysis, content+"changed")
		} else {
			_, err = f.s.RestorePrompt(t.Context(), client.Analysis)
		}
		promptError(t, err, service.ErrorUnavailable, "service.storage_failure")
		if modelScalar[string](t, f, `SELECT content FROM prompts WHERE key='analysis'`) != content || promptEventCount(f) != count {
			t.Fatal("failed prompt mutation committed or emitted")
		}
		f.exec(t, `DROP TRIGGER reject_prompt`)
	}
	f.exec(t, `CREATE TABLE prompt_commit_parent(id INTEGER PRIMARY KEY)`)
	f.exec(t, `CREATE TABLE prompt_commit_child(parent_id INTEGER REFERENCES prompt_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	f.exec(t, `CREATE TRIGGER reject_prompt_commit AFTER INSERT ON prompts BEGIN INSERT INTO prompt_commit_child VALUES(1); END`)
	_, err := f.s.SavePrompt(t.Context(), client.Market, "market custom")
	promptError(t, err, service.ErrorUnavailable, "service.storage_failure")
	if modelScalar[int](t, f, `SELECT count(*) FROM prompts WHERE key='market'`) != 0 || modelScalar[int](t, f, `SELECT count(*) FROM prompt_commit_child`) != 0 || promptEventCount(f) != count {
		t.Fatal("commit failure left partial prompt state or a false event")
	}
}

func TestPromptPublishFailureDoesNotReportSavedMutationAsFailed(t *testing.T) {
	f := newModelFixture(t)
	f.onPublish = func(string) error { return errors.New("private callback failure") }
	view, err := f.s.SavePrompt(t.Context(), client.Market, "saved text")
	if err != nil || !view.Customized || modelScalar[string](t, f, `SELECT content FROM prompts WHERE key='market'`) != "saved text" || promptEventCount(f) != 1 {
		t.Fatal("event failure was reported as a failed save", view, err)
	}
	if view, err = f.s.RestorePrompt(t.Context(), client.Market); err != nil || view.Customized || promptEventCount(f) != 2 {
		t.Fatal("event failure was reported as a failed restore", view, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.onPublish = func(string) error { cancel(); return ctx.Err() }
	view, err = f.s.SavePrompt(ctx, client.Market, "committed before cancellation")
	if err != nil || !view.Customized || modelScalar[string](t, f, `SELECT content FROM prompts WHERE key='market'`) != "committed before cancellation" {
		t.Fatal("post-commit cancellation was reported as an unsaved mutation", view, err)
	}
}

func promptOperations(s *Service) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"list":     func(ctx context.Context) error { _, err := s.ListPrompts(ctx); return err },
		"snapshot": func(ctx context.Context) error { _, err := s.PromptSnapshot(ctx); return err },
		"save":     func(ctx context.Context) error { _, err := s.SavePrompt(ctx, client.Market, "text"); return err },
		"restore":  func(ctx context.Context) error { _, err := s.RestorePrompt(ctx, client.Market); return err },
	}
}

func waitForPromptDatabaseWait(t *testing.T, f *modelFixture, previousWaits int64) {
	t.Helper()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		f.s.mu.Lock()
		jobs := f.s.jobs
		f.s.mu.Unlock()
		if jobs != 0 && f.db.Stats().WaitCount > previousWaits {
			return
		}
		select {
		case <-timeout.C:
			t.Fatal("prompt operation did not wait for the occupied database connection")
		case <-ticker.C:
		}
	}
}

func TestPromptOperationsPreserveCancellationWhileWaitingForDatabase(t *testing.T) {
	for _, name := range []string{"list", "snapshot", "save", "restore"} {
		t.Run(name, func(t *testing.T) {
			f := newModelFixture(t)
			operation := promptOperations(f.s)[name]
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := operation(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal("lost prior cancellation", err)
			}
			f.db.SetMaxOpenConns(1)
			connection, err := f.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			ctx, cancel = context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			waits := f.db.Stats().WaitCount
			go func() { result <- operation(ctx) }()
			waitForPromptDatabaseWait(t, f, waits)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("lost in-flight cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled prompt operation did not exit")
			}
			connection.Close()
			if modelScalar[int](t, f, `SELECT count(*) FROM prompts`) != 0 || promptEventCount(f) != 0 {
				t.Fatal("cancelled operation wrote or emitted")
			}
		})
	}
}

func TestPromptCloseCancelsActiveWorkAndRejectsEveryNewOperation(t *testing.T) {
	f := newModelFixture(t)
	f.db.SetMaxOpenConns(1)
	connection, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	result := make(chan error, 1)
	waits := f.db.Stats().WaitCount
	go func() { _, err := f.s.SavePrompt(t.Context(), client.Market, "text"); result <- err }()
	waitForPromptDatabaseWait(t, f, waits)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("close did not cancel active prompt work", err)
	}
	connection.Close()
	for _, operation := range promptOperations(f.s) {
		promptError(t, operation(t.Context()), service.ErrorUnavailable, "runtime.closed")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM prompts`) != 0 || promptEventCount(f) != 0 {
		t.Fatal("closed prompt work wrote or emitted")
	}
}
