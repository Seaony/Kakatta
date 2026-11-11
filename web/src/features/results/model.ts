import type { ResultsSearch } from '../../app/search';
import type { Schema, RequestQuery } from '../../api/types';
import type { Locale } from '../../i18n';
import { formatDateTime } from '../../i18n/format';

export type Finding = Schema<'FindingView'>;
export type Listing = Schema<'Listing'>;
export type SavedListing = Schema<'ListingView'>;

export function safeExternalURL(value: string | null): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return (url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password ? url.href : undefined;
  } catch { return undefined; }
}

export function listingThumbnailURL(listing: Listing, findingId?: number): string | undefined {
  if (!safeExternalURL(listing.thumbnail_url)) return undefined;
  const query = findingId === undefined ? '' : `?finding_id=${findingId}`;
  return `/api/v1/listings/${encodeURIComponent(listing.item_id)}/thumbnail${query}`;
}

export function resultTime(value: number, locale: Locale, timeZone?: string): string {
  return timeZone === undefined ? new Date(value).toISOString() : `${formatDateTime(value, locale, timeZone)} (${timeZone})`;
}

export function findingQuery(search: ResultsSearch): RequestQuery<'listFindings'> {
  return {
    ...(search.task_id === undefined ? {} : { task_id: search.task_id }),
    ...(search.view === undefined ? {} : { view: search.view }),
    ...(search.decision === undefined ? {} : { decision: search.decision }),
    ...(search.price_drop === undefined ? {} : { price_drop: search.price_drop }),
    ...(search.over_budget === undefined ? {} : { over_budget: search.over_budget }),
    ...(search.q === undefined ? {} : { q: search.q }),
    ...(search.sort === undefined ? {} : { sort: search.sort }),
    ...(search.limit === undefined ? {} : { limit: search.limit }),
    ...(search.cursor === undefined ? {} : { cursor: search.cursor }),
  };
}

export function detailQuery(search: ResultsSearch): RequestQuery<'getFinding'> {
  const { limit: _limit, cursor: _cursor, ...query } = findingQuery(search);
  if (query.task_id === undefined && query.view === undefined) return { ...query, view: 'default' };
  return query;
}

export function findingsExportQuery(search: ResultsSearch): RequestQuery<'exportTaskFindings'> {
  const { task_id: _task_id, limit: _limit, cursor: _cursor, ...query } = findingQuery(search);
  return query;
}

export function listingQuery(search: ResultsSearch): RequestQuery<'listListings'> {
  return {
    ...(search.collection === 'favorites' ? { favorite: true } : {}),
    ...(search.collection === 'dismissed' ? { dismissed: true } : {}),
    ...(search.q === undefined ? {} : { q: search.q }),
    ...(search.limit === undefined ? {} : { limit: search.limit }),
    ...(search.cursor === undefined ? {} : { cursor: search.cursor }),
  };
}

export function latestDeliveries(deliveries: Schema<'DeliverySummary'>[]) {
  const channels = new Map<string, Schema<'DeliverySummary'>>();
  for (const delivery of deliveries) {
    const key = delivery.channel_id === null ? `deleted-${delivery.id}` : String(delivery.channel_id);
    const previous = channels.get(key);
    if (!previous || (delivery.at ?? 0) > (previous.at ?? 0) ||
      (delivery.at === previous.at && delivery.id > previous.id)) channels.set(key, delivery);
  }
  return [...channels.values()].sort((a, b) => (b.at ?? 0) - (a.at ?? 0) || b.id - a.id);
}

export function isOverBudget(finding: Finding): boolean {
  return finding.task.budget_max !== null && finding.listing.price !== null &&
    finding.listing.price > finding.task.budget_max;
}

export function selectableIDs(findings: Finding[]): number[] {
  return [...new Set(findings.map((finding) => finding.id))].slice(0, 200);
}

export function effectiveRisks(finding: Finding) {
  return (finding.task.profile.risks ?? []).map((risk) => {
    const check = finding.checks.find((entry) => entry.key === risk.id);
    return { risk, status: check?.status ?? 'unknown', source: check?.source ?? 'unknown' };
  });
}
