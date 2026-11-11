import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useUnsavedChanges } from '../../app/unsaved-changes';
import { Button, ConfirmDialog, FormSection, StatusBadge, TextAreaField } from '../../components/ui';
import { useTranslation } from '../../i18n';
import { missingPromptKeys, type PromptKind } from './draft';

export type PromptEditorProps = {
  kind: PromptKind;
  content: string;
  systemSuffix: string;
  customized: boolean;
  requiredKeys: readonly string[];
  pending?: boolean;
  error?: string;
  fieldError?: string;
  onSave(content: string): Promise<string>;
  onRestore(): Promise<string>;
  onDirtyChange?(dirty: boolean): void;
};

export function PromptEditor({ kind, content, systemSuffix, customized, requiredKeys, pending = false, error, fieldError, onSave, onRestore, onDirtyChange }: PromptEditorProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(content);
  const [baseline, setBaseline] = useState(content);
  const [restoreOpen, setRestoreOpen] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const lastContent = useRef(content);
  const editorRef = useRef<HTMLTextAreaElement>(null);
  const dirty = draft !== baseline;
  useUnsavedChanges(dirty);
  const missing = missingPromptKeys(draft, requiredKeys);
  useEffect(() => {
    if (lastContent.current === content) return;
    lastContent.current = content;
    if (!dirty) setDraft(content);
    setBaseline(content);
  }, [content, dirty]);
  useEffect(() => { onDirtyChange?.(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => { if (fieldError) editorRef.current?.focus(); }, [fieldError]);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setSubmitted(true);
    if (pending) return;
    if (missing.length > 0) { editorRef.current?.focus(); return; }
    try { const saved = await onSave(draft); setDraft(saved); setBaseline(saved); setSubmitted(false); }
    catch { /* 失败由调用方保留原因与当前输入。 */ }
  }
  return <div className="min-w-0 space-y-lg">
    <StatusBadge tone={customized ? 'accent' : 'neutral'}>{t(customized ? 'web.ai.prompt_custom' : 'web.ai.prompt_builtin')}</StatusBadge>
    <form onSubmit={(event) => { void submit(event); }} className="min-w-0 space-y-lg" aria-busy={pending}>
      <TextAreaField ref={editorRef} id={`prompt-${kind}`} label={t('web.ai.prompt_content')} value={draft} onChange={(event) => setDraft(event.target.value)} rows={18} disabled={pending} spellCheck={false} autoComplete="off" autoCapitalize="none" autoCorrect="off" hint={t('web.ai.prompt_next_run')} {...(submitted && missing.length > 0 ? { error: t('web.ai.prompt_missing_key', { key: missing.join(', ') }) } : fieldError ? { error: fieldError } : {})} />
      <p className="text-body text-text-secondary">{t('web.ai.prompt_task_profile_hint')}</p>
      {error && <p role="alert" className="text-body text-danger">{error}</p>}
      <div className="flex flex-wrap gap-sm">
        <Button type="submit" variant="primary" disabled={!dirty} busy={pending} busyLabel={t('web.confirm_pending')}>{t('web.ai.save')}</Button>
        <Button disabled={pending || !customized && !dirty} onClick={() => setRestoreOpen(true)}>{t('web.ai.restore')}</Button>
      </div>
    </form>
    <FormSection title={t('web.ai.prompt_system_suffix')} description={t('web.ai.prompt_system_suffix_hint')}>
      <p className="whitespace-pre-wrap break-words text-body text-text-primary">{systemSuffix}</p>
    </FormSection>
    <ConfirmDialog open={restoreOpen} onOpenChange={(open) => { if (!pending) setRestoreOpen(open); }} title={t('web.ai.restore')} description={t('web.ai.prompt_restore_confirm')} confirmLabel={t('web.ai.restore')} cancelLabel={t('common.cancel')} pending={pending} {...(error ? { error } : {})} onConfirm={() => {
      void onRestore().then((restored) => { setDraft(restored); setBaseline(restored); setRestoreOpen(false); setSubmitted(false); }).catch(() => {});
    }} />
  </div>;
}
