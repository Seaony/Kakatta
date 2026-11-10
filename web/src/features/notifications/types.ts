import type { components } from '../../api/schema.gen';
import type { ChannelKind } from './catalog';

export type SecretEdit = { mode: 'keep' | 'replace' | 'clear'; value: string };
// 表单草稿只保留编辑值；由正式描述的适配器转成请求，不把草稿当 HTTP 请求。
export type ChannelDraft = { kind: ChannelKind; name: string; enabled: boolean; fields: Record<string, string>; secrets: Record<string, SecretEdit> };
export type ChannelRow = { id: number; kind: ChannelKind; name: string; enabled: boolean; fields: Record<string, string>; hasSecret: Record<string, boolean>; testedAt: number | null; testOK: boolean | null; testError: components['schemas']['LocalizedMessage'] | null };
export type DeliveryItem = components['schemas']['DeliveryItem'];
export type TestReceipt = components['schemas']['ChannelTestResult'];
// 适配器只传安全页面数据。真实实现从生成的 schema 映射，不回传秘密。
export interface ChannelAdapter {
  get(id: number, signal: AbortSignal): Promise<ChannelRow>;
  list(signal: AbortSignal): Promise<ChannelRow[]>;
  save(id: number | null, draft: ChannelDraft, signal: AbortSignal): Promise<ChannelRow>;
  setEnabled(id: number, enabled: boolean, signal: AbortSignal): Promise<ChannelRow>;
  remove(id: number, signal: AbortSignal): Promise<void>;
  test(id: number, signal: AbortSignal): Promise<TestReceipt>;
  history(id: number, signal: AbortSignal): Promise<components['schemas']['DeliverySummary'][]>;
  deliveryItems(deliveryId: number, signal: AbortSignal): Promise<DeliveryItem[]>;
}
export type ChannelSetupProps = { adapter?: ChannelAdapter | undefined; onSaved?(channelId: number): void; onAvailabilityChange?(hasSavedChannel: boolean | null): void };
