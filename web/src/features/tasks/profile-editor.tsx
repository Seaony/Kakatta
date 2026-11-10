import { useRef, useState, type ReactNode } from 'react';
import type { Schema } from '../../api/types';
import { useMessageText } from '../../api/hooks';
import { useTranslation } from '../../i18n';
import { Button, Checkbox, Dialog, InlineMessage, RadioGroup, SelectField, StatusBadge, TextAreaField, TextField } from '../../components/ui';
import { profileRow, splitWords, type ProfileDraft, type ProfileRow } from './model';

const sourceKeys = { user: 'tasks.profile.user_source', ai: 'tasks.profile.ai_source', system: 'tasks.profile.system_source' } as const;
const attitudeKeys = {
  confirm: ['tasks.risk.confirm', 'tasks.risk.confirm_hint'],
  reject: ['tasks.risk.reject', 'tasks.risk.reject_hint'],
  mind: ['tasks.risk.mind', 'tasks.risk.mind_hint'],
  ignore: ['tasks.risk.ignore', 'tasks.risk.ignore_hint'],
} as const;
const categoryKeys: Readonly<Record<string, 'analysis.risk_category.authenticity' | 'analysis.risk_category.locks' | 'analysis.risk_category.condition' | 'analysis.risk_category.description' | 'analysis.risk_category.transaction' | 'analysis.risk_category.seller'>> = {
  '真伪和来源': 'analysis.risk_category.authenticity', '锁和账号': 'analysis.risk_category.locks', '状态': 'analysis.risk_category.condition',
  '描述可信度': 'analysis.risk_category.description', '交易方式': 'analysis.risk_category.transaction', '卖家': 'analysis.risk_category.seller',
};

function attitude(value: string): Schema<'AnalysisAttitude'> | undefined {
  switch (value) { case 'confirm': case 'reject': case 'mind': case 'ignore': return value; default: return undefined; }
}

function replaceRow<T>(rows: ProfileRow<T>[], key: string, update: (value: T) => T): ProfileRow<T>[] {
  return rows.map((row) => row.key === key ? { ...row, value: update(row.value) } : row);
}

export type ProfileEditorProps = {
  value: ProfileDraft;
  onChange(value: ProfileDraft): void;
  dictionary: Schema<'AnalysisRiskDefinition'>[];
  disabled?: boolean;
  error?: string;
  generation?: ReactNode;
};

export function ProfileEditor({ value, onChange, dictionary, disabled = false, error, generation }: ProfileEditorProps) {
  const { t } = useTranslation();
  const messageText = useMessageText();
  const dictionaryName = (check: Schema<'AnalysisRiskDefinition'>) => messageText({ code: `analysis.risk_name.${check.id}`, args: {} });
  const dictionaryHint = (check: Schema<'AnalysisRiskDefinition'>) => messageText({ code: `analysis.risk_hint.${check.id}`, args: {} });
  const dictionaryQuestion = (check: Schema<'AnalysisRiskDefinition'>) => check.question ? messageText({ code: `analysis.question.${check.id}`, args: {} }) : '';
  const categoryName = (category: string) => { const key = categoryKeys[category]; return key ? t(key) : category; };
  const [dictionaryOpen, setDictionaryOpen] = useState(false);
  const [dictionarySearch, setDictionarySearch] = useState('');
  const [dictionaryCategory, setDictionaryCategory] = useState('');
  const [optionDrafts, setOptionDrafts] = useState<Record<string, string>>({});
  const section = useRef<HTMLDivElement>(null);
  const named = (name: string) => name || t('tasks.profile.untitled');
  const focusSection = () => section.current?.querySelector<HTMLElement>('h3')?.focus();
  const remove = (group: 'specs' | 'musts' | 'bonuses' | 'risks', key: string) => {
    onChange({ ...value, [group]: value[group].filter((row) => row.key !== key) });
    focusSection();
  };
  const moveSpec = (index: number, delta: number) => {
    const specs = [...value.specs];
    const current = specs[index], target = specs[index + delta];
    if (!current || !target) return;
    specs[index] = { ...target, value: { ...target.value, edited: true } };
    specs[index + delta] = { ...current, value: { ...current.value, edited: true } };
    onChange({ ...value, specs });
  };
  const addDictionary = (check: Schema<'AnalysisRiskDefinition'>) => {
    if (value.risks.some((row) => row.value.id === check.id)) return;
    onChange({ ...value, risks: [...value.risks, profileRow({ id: check.id, attitude: check.default_attitude, edited: true, source: 'user', rev: 1 })] });
  };
  const categories = [...new Set(dictionary.map((check) => check.category))];
  const matchingDictionary = dictionary.filter((check) => (!dictionaryCategory || check.category === dictionaryCategory) && `${dictionaryName(check)} ${dictionaryHint(check)}`.toLocaleLowerCase().includes(dictionarySearch.toLocaleLowerCase()));

  return <div ref={section} className="min-w-0 space-y-xl">
    {generation}
    {error && <InlineMessage tone="danger">{error}</InlineMessage>}
    {value.summary && <p className="max-w-prose text-body text-text-primary">{value.summary}</p>}
    <section className="min-w-0 space-y-lg" aria-labelledby="task-profile-specs">
      <div className="flex flex-wrap items-center justify-between gap-md">
        <div className="min-w-0"><h3 id="task-profile-specs" tabIndex={-1} className="text-section font-semibold">{t('tasks.profile.specs')}</h3><p className="text-body text-text-secondary">{t('tasks.profile.specs_hint')}</p></div>
        <Button disabled={disabled} onClick={() => onChange({ ...value, specs: [...value.specs, profileRow({ name: '', options: [], values: [], source: 'user', edited: true })] })}>{t('tasks.profile.add_spec')}</Button>
      </div>
      {value.specs.length === 0 && <p className="text-body text-text-secondary">{t('tasks.profile.no_specs')}</p>}
      {value.specs.map((row, index) => <div key={row.key} className="min-w-0 space-y-md border-t border-border pt-lg">
        <div className="flex flex-wrap items-end gap-md">
          <div className="min-w-0 flex-1"><TextField label={t('tasks.profile.spec_name')} value={row.value.name} disabled={disabled} onChange={(event) => onChange({ ...value, specs: replaceRow(value.specs, row.key, (spec) => ({ ...spec, name: event.target.value, edited: true })) })} /></div>
          <StatusBadge tone={row.value.source === 'user' ? 'neutral' : 'info'}>{t(sourceKeys[row.value.source])}</StatusBadge>
          <Button variant="plain" disabled={disabled || index === 0} aria-label={t('tasks.profile.move_up_named', { name: named(row.value.name) })} onClick={() => moveSpec(index, -1)}>{t('tasks.profile.move_up')}</Button>
          <Button variant="plain" disabled={disabled || index === value.specs.length - 1} aria-label={t('tasks.profile.move_down_named', { name: named(row.value.name) })} onClick={() => moveSpec(index, 1)}>{t('tasks.profile.move_down')}</Button>
          <Button variant="plain" disabled={disabled} aria-label={t('tasks.profile.remove_named', { name: named(row.value.name) })} onClick={() => remove('specs', row.key)}>{t('tasks.profile.remove')}</Button>
        </div>
        <TextAreaField label={t('tasks.profile.options')} hint={t('tasks.field.words_hint')} rows={2} disabled={disabled} value={optionDrafts[row.key] ?? row.value.options.join('\n')} onChange={(event) => setOptionDrafts({ ...optionDrafts, [row.key]: event.target.value })} onBlur={() => {
          if (optionDrafts[row.key] === undefined) return;
          const options = splitWords(optionDrafts[row.key] ?? row.value.options.join('\n'));
          if (options.length === row.value.options.length && options.every((option, index) => option === row.value.options[index])) return;
          onChange({ ...value, specs: replaceRow(value.specs, row.key, (spec) => ({ ...spec, options, values: spec.values.filter((item) => options.includes(item)), edited: true })) });
        }} />
        <fieldset className="min-w-0 space-y-sm"><legend className="text-body font-medium">{t('tasks.profile.accepted_values')}</legend>
          <Checkbox label={t('tasks.profile.any_spec')} checked={row.value.values.length === 0} disabled={disabled} onCheckedChange={(checked) => { if (checked) onChange({ ...value, specs: replaceRow(value.specs, row.key, (spec) => ({ ...spec, values: [], edited: true })) }); }} />
          <div className="flex flex-wrap gap-md">{row.value.options.map((option) => <Checkbox key={option} label={option} checked={row.value.values.includes(option)} disabled={disabled} onCheckedChange={(checked) => onChange({ ...value, specs: replaceRow(value.specs, row.key, (spec) => ({ ...spec, values: checked ? [...spec.values, option] : spec.values.filter((item) => item !== option), edited: true })) })} />)}</div>
        </fieldset>
      </div>)}
    </section>
    {(['musts', 'bonuses'] as const).map((group) => <section key={group} className="min-w-0 space-y-lg border-t border-border pt-xl" aria-labelledby={`task-profile-${group}`}>
      <div className="flex flex-wrap items-center justify-between gap-md"><h3 id={`task-profile-${group}`} tabIndex={-1} className="text-section font-semibold">{t(group === 'musts' ? 'tasks.profile.musts' : 'tasks.profile.bonuses')}</h3><Button disabled={disabled} onClick={() => onChange({ ...value, [group]: [...value[group], profileRow({ id: '', text: '', source: 'user', edited: true, rev: 1 })] })}>{t(group === 'musts' ? 'tasks.profile.add_must' : 'tasks.profile.add_bonus')}</Button></div>
      {value[group].length === 0 && <p className="text-body text-text-secondary">{t('tasks.profile.no_conditions')}</p>}
      {value[group].map((row, index) => <div key={row.key} className="flex min-w-0 flex-wrap items-end gap-md">
        <div className="min-w-0 flex-1"><TextField label={t(group === 'musts' ? 'tasks.profile.must_number' : 'tasks.profile.bonus_number', { number: index + 1 })} disabled={disabled} value={row.value.text} onChange={(event) => onChange({ ...value, [group]: replaceRow(value[group], row.key, (item) => ({ ...item, text: event.target.value, edited: true })) })} /></div>
        <StatusBadge tone={row.value.source === 'user' ? 'neutral' : 'info'}>{t(sourceKeys[row.value.source])}</StatusBadge>
        <Button variant="plain" disabled={disabled} aria-label={t('tasks.profile.remove_named', { name: named(row.value.text) })} onClick={() => remove(group, row.key)}>{t('tasks.profile.remove')}</Button>
      </div>)}
    </section>)}
    <section className="min-w-0 space-y-lg border-t border-border pt-xl" aria-labelledby="task-profile-risks">
      <div className="flex flex-wrap items-center justify-between gap-md"><div className="min-w-0"><h3 id="task-profile-risks" tabIndex={-1} className="text-section font-semibold">{t('tasks.profile.risks')}</h3><p className="text-body text-text-secondary">{t('tasks.profile.risks_hint')}</p></div><div className="flex flex-wrap gap-sm"><Button disabled={disabled || dictionary.length === 0} onClick={() => setDictionaryOpen(true)}>{t('tasks.profile.choose_dictionary')}</Button><Button disabled={disabled} onClick={() => onChange({ ...value, risks: [...value.risks, profileRow({ id: '', name: '', hint: '', question: '', attitude: 'mind', source: 'user', edited: true, rev: 1 })] })}>{t('tasks.profile.add_risk')}</Button></div></div>
      {value.risks.length === 0 && <p className="text-body text-text-secondary">{t('tasks.profile.no_risks')}</p>}
      {value.risks.map((row) => {
        const definition = dictionary.find((check) => check.id === row.value.id);
        const name = definition ? dictionaryName(definition) : row.value.name ?? '';
        return <div key={row.key} className="min-w-0 space-y-md border-t border-border pt-lg">
          <div className="flex flex-wrap items-start justify-between gap-md"><div className="min-w-0 flex-1 space-y-sm">{definition ? <h4 className="text-body font-semibold">{name}</h4> : <TextField label={t('tasks.profile.risk_name')} value={name} disabled={disabled} onChange={(event) => onChange({ ...value, risks: replaceRow(value.risks, row.key, (risk) => ({ ...risk, name: event.target.value, edited: true })) })} />}<StatusBadge tone={row.value.source === 'user' ? 'neutral' : 'info'}>{t(sourceKeys[row.value.source])}</StatusBadge></div><Button variant="plain" disabled={disabled} aria-label={t('tasks.profile.remove_named', { name: named(name) })} onClick={() => remove('risks', row.key)}>{t('tasks.profile.remove')}</Button></div>
          {definition ? <div className="space-y-sm text-body text-text-secondary"><p>{dictionaryHint(definition)}</p>{definition.question && <p>{dictionaryQuestion(definition)}</p>}</div> : <div className="grid min-w-0 gap-lg md:grid-cols-2"><TextAreaField label={t('tasks.profile.risk_hint')} value={row.value.hint ?? ''} disabled={disabled} onChange={(event) => onChange({ ...value, risks: replaceRow(value.risks, row.key, (risk) => ({ ...risk, hint: event.target.value, edited: true })) })} /><TextAreaField label={t('tasks.profile.risk_question')} value={row.value.question ?? ''} disabled={disabled} onChange={(event) => onChange({ ...value, risks: replaceRow(value.risks, row.key, (risk) => ({ ...risk, question: event.target.value, edited: true })) })} /></div>}
          <RadioGroup label={t('tasks.profile.attitude_for', { name: named(name) })} value={row.value.attitude} disabled={disabled} className="grid min-w-0 grid-cols-1 gap-sm sm:grid-cols-2" options={Object.entries(attitudeKeys).map(([key, [label, hint]]) => ({ value: key, label: t(label), description: t(hint) }))} onValueChange={(next) => {
            const selected = attitude(next);
            if (selected) onChange({ ...value, risks: replaceRow(value.risks, row.key, (risk) => ({ ...risk, attitude: selected, edited: true })) });
          }} />
        </div>;
      })}
    </section>
    <Dialog open={dictionaryOpen} onOpenChange={setDictionaryOpen} title={t('tasks.profile.dictionary_title')} closeLabel={t('common.close')}>
      <div className="space-y-lg"><TextField label={t('tasks.profile.dictionary_search')} value={dictionarySearch} onChange={(event) => setDictionarySearch(event.target.value)} /><SelectField label={t('tasks.profile.dictionary_category')} value={dictionaryCategory} onValueChange={setDictionaryCategory} options={[{ value: '', label: t('tasks.profile.all_categories') }, ...categories.map((category) => ({ value: category, label: categoryName(category) }))]} />
        {matchingDictionary.length === 0 && <p className="text-body text-text-secondary">{t('tasks.profile.dictionary_empty')}</p>}
        {matchingDictionary.map((check) => {
          const exists = value.risks.some((row) => row.value.id === check.id);
          return <div key={check.id} className="space-y-sm border-t border-border py-lg"><div className="flex flex-wrap items-start justify-between gap-md"><div className="min-w-0 flex-1"><h3 className="text-body font-semibold">{dictionaryName(check)}</h3><p className="text-body text-text-secondary">{dictionaryHint(check)}</p></div><Button disabled={disabled || exists} onClick={() => addDictionary(check)}>{t(exists ? 'tasks.profile.already_added' : 'tasks.profile.add')}</Button></div><p className="text-body text-text-secondary">{dictionaryQuestion(check)}</p><StatusBadge>{t(attitudeKeys[check.default_attitude][0])}</StatusBadge></div>;
        })}
      </div>
    </Dialog>
  </div>;
}
