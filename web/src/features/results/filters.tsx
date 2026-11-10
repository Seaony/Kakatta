import { useEffect, useRef, useState } from 'react';
import type { ResultsSearch } from '../../app/search';
import type { Schema } from '../../api/types';
import { Button, Checkbox, SelectField, TextField } from '../../components/ui';
import { useTranslation } from '../../i18n';

const views = [
  { value: 'default', key: 'results.filter.default' },
  { value: 'other', key: 'results.filter.unrecommended' },
  { value: 'all', key: 'results.filter.all' },
  { value: 'hidden', key: 'results.filter.hidden' },
] as const;
const decisions = [
  { value: '', key: 'results.filter.any' },
  { value: 'pending', key: 'results.filter.pending' },
  { value: 'scored', key: 'results.filter.scored' },
  { value: 'matched', key: 'results.filter.matched' },
  { value: 'rejected', key: 'results.filter.rejected' },
  { value: 'failed', key: 'results.filter.failed' },
  { value: 'skipped', key: 'results.filter.skipped' },
] as const;

type Props = {
  search: ResultsSearch;
  tasks: Schema<'TaskView'>[];
  tasksHaveMore: boolean;
  tasksLoading: boolean;
  onMoreTasks(): void;
  onChange(patch: Partial<ResultsSearch>): void;
};

export function ResultsFilters({ search, tasks, tasksHaveMore, tasksLoading, onMoreTasks, onChange }: Props) {
  const { t } = useTranslation();
  const [text, setText] = useState(search.q ?? '');
  const composing = useRef(false);
  useEffect(() => setText(search.q ?? ''), [search.q]);
  const collection = search.collection;
  return (
    <section className="space-y-lg border-y border-border py-lg" aria-label={t('results.filter.view')}>
      <div className="flex flex-wrap gap-sm" role="group" aria-label={t('results.collection.findings')}>
        <Button variant={collection === undefined ? 'primary' : 'secondary'} aria-pressed={collection === undefined}
          onClick={() => onChange({ collection: undefined })}>{t('results.collection.findings')}</Button>
        <Button variant={collection === 'favorites' ? 'primary' : 'secondary'} aria-pressed={collection === 'favorites'}
          onClick={() => onChange({ collection: 'favorites' })}>{t('results.collection.favorites')}</Button>
        <Button variant={collection === 'dismissed' ? 'primary' : 'secondary'} aria-pressed={collection === 'dismissed'}
          onClick={() => onChange({ collection: 'dismissed' })}>{t('results.collection.dismissed')}</Button>
      </div>
      <div className="grid min-w-0 gap-lg md:grid-cols-2">
        <div className="space-y-sm">
          <SelectField label={t('results.task')} value={search.task_id === undefined ? '' : String(search.task_id)} disabled={collection !== undefined}
            options={[{ value: '', label: t('results.task.all') }, ...tasks.map((task) => ({ value: String(task.id), label: task.name }))]}
            onValueChange={(value) => onChange({ task_id: tasks.find((task) => String(task.id) === value)?.id })} />
          {tasksHaveMore && collection === undefined && <Button busy={tasksLoading} onClick={onMoreTasks}>{t('results.task.more')}</Button>}
        </div>
        <form className="flex min-w-0 flex-wrap items-end gap-sm" onSubmit={(event) => {
          event.preventDefault();
          if (!composing.current) onChange({ q: text.trim() || undefined });
        }}>
          <div className="min-w-0 flex-1">
            <TextField label={t('results.search')} type="search" value={text} onChange={(event) => setText(event.target.value)}
              onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; }}
              onKeyDown={(event) => { if (event.key === 'Enter' && (event.nativeEvent.isComposing || composing.current)) event.preventDefault(); }} />
          </div>
          <Button type="submit">{t('results.search.submit')}</Button>
        </form>
      </div>
      {collection === undefined && <>
        <div className="grid min-w-0 gap-lg sm:grid-cols-2 lg:grid-cols-3">
          <SelectField label={t('results.filter.view')} value={search.view ?? 'default'} options={views.map((view) => ({ value: view.value, label: t(view.key) }))}
            onValueChange={(value) => { const view = views.find((entry) => entry.value === value); if (view) onChange({ view: view.value }); }} />
          <SelectField label={t('results.filter.decision')} value={search.decision ?? ''} options={decisions.map((entry) => ({ value: entry.value, label: t(entry.key) }))}
            onValueChange={(value) => { const decision = decisions.find((entry) => entry.value === value); if (decision) onChange({ decision: decision.value || undefined, view: decision.value ? 'all' : search.view }); }} />
          <SelectField label={t('results.sort')} value={search.sort ?? 'score'}
            options={[{ value: 'score', label: t('results.sort.score') }, { value: 'latest', label: t('results.sort.found') }]}
            onValueChange={(value) => { if (value === 'score' || value === 'latest') onChange({ sort: value }); }} />
        </div>
        <div className="flex flex-wrap gap-lg">
          <Checkbox label={t('results.filter.price_drop')} checked={search.price_drop === true} onCheckedChange={(checked) => onChange({ price_drop: checked || undefined })} />
          <Checkbox label={t('results.filter.over_budget')} checked={search.over_budget === true} onCheckedChange={(checked) => onChange({ over_budget: checked || undefined, view: checked ? 'all' : search.view })} />
        </div>
      </>}
      {(search.q || search.decision || search.price_drop || search.over_budget || search.view && search.view !== 'default') && (
        <Button variant="plain" onClick={() => onChange({ q: undefined, decision: undefined, price_drop: undefined, over_budget: undefined, view: undefined })}>
          {t('results.empty.clear')}
        </Button>
      )}
    </section>
  );
}
