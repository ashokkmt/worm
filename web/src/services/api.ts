import { SystemStats, HealthStatus } from '../types/telemetry';
import { NormalizedEvent, EventTrace, QuarantineEntry, SourceSummary } from '../types/event';
import { ParserPackSummary, PackValidationResponse, MarketplacePack } from '../types/pack';
import { RuntimeConfig } from '../types/config';
import { ConnectionSummary, ConnectionValidationResponse, ConnectionTestResponse, ConnectionApplyResponse } from '../types/connection';

const BASE_URL = '/api/v1';
let adminToken = '';

export function setAdminToken(token: string): void {
  adminToken = token;
}

async function fetchJSON<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${endpoint}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(adminToken ? { Authorization: `Bearer ${adminToken}` } : {}),
      ...options?.headers,
    },
  });

  if (!res.ok) {
    let errMessage = `HTTP ${res.status}: ${res.statusText}`;
    try {
      const errObj = await res.json();
      if (errObj.error) errMessage = errObj.error;
    } catch {
      // ignore
    }
    throw new Error(errMessage);
  }

  return res.json();
}

async function fetchText(endpoint: string, options?: RequestInit): Promise<string> {
  const res = await fetch(`${BASE_URL}${endpoint}`, options);
  if (!res.ok) {
    let errMessage = `HTTP ${res.status}: ${res.statusText}`;
    try {
      const errObj = await res.json();
      if (errObj.error) errMessage = errObj.error;
    } catch {
      // ignore
    }
    throw new Error(errMessage);
  }
  return res.text();
}

export const apiService = {
  setAdminToken,
  getHealth: () => fetchJSON<HealthStatus>('/health'),
  getStats: () => fetchJSON<SystemStats>('/stats'),
  
  listEvents: (params?: { category?: string; severity?: string; q?: string; limit?: number; offset?: number }) => {
    const q = new URLSearchParams();
    if (params?.category && params.category !== 'all') q.set('category', params.category);
    if (params?.severity && params.severity !== 'all') q.set('severity', params.severity);
    if (params?.q) q.set('q', params.q);
    if (params?.limit) q.set('limit', params.limit.toString());
    if (params?.offset) q.set('offset', params.offset.toString());
    const qs = q.toString();
    return fetchJSON<{ events: NormalizedEvent[]; total: number; limit: number; offset: number }>(`/events${qs ? `?${qs}` : ''}`);
  },

  getEvent: (id: string) => fetchJSON<NormalizedEvent>(`/events/${encodeURIComponent(id)}`),
  getEventTrace: (id: string) => fetchJSON<EventTrace>(`/events/${encodeURIComponent(id)}/trace`),

  verifyRaw: (rawId: string) => fetchJSON<{
    raw_id: string;
    matches: boolean;
    stored_sha256: string;
    computed_sha256: string;
    status: string;
  }>(`/raw/${encodeURIComponent(rawId)}/verify`, { method: 'POST' }),

  verifyEvent: (eventId: string) => fetchJSON<{
    event_id: string;
    raw_id: string;
    stored_hash: string;
    computed_hash: string;
    hash_matches: boolean;
    stored_message: string;
    expected_message: string;
    message_matches: boolean;
    raw_valid: boolean;
    passed: boolean;
    status: string;
    failure_reason?: string;
  }>(`/events/${encodeURIComponent(eventId)}/verify`, { method: 'POST' }),

  listQuarantine: (limit = 50, offset = 0) =>
    fetchJSON<{ quarantine: QuarantineEntry[]; total: number; limit: number; offset: number }>(
      `/quarantine?limit=${limit}&offset=${offset}`
    ),

  replayQuarantine: (quarantineId: string) =>
    fetchJSON<{ status: string; quarantine_id: string; event: NormalizedEvent }>(
      `/quarantine/${encodeURIComponent(quarantineId)}/replay`,
      { method: 'POST' }
    ),

  replayAllQuarantine: (force = false) =>
    fetchJSON<{
      total: number;
      replayed: number;
      failed: number;
      details: Array<{ quarantine_id: string; status: string; event_id?: string; error?: string }>;
    }>(`/quarantine/replay-all${force ? '?force=true' : ''}`, { method: 'POST' }),

  listSources: () => fetchJSON<{ sources: SourceSummary[] }>('/sources'),

  listPacks: () => fetchJSON<{ version: string; packs: ParserPackSummary[] }>('/packs'),
  listMarketplace: (q = '', category = '', format = '', offset=0, filters:{vendor?:string;product?:string;model?:string}={}) => {
    const params = new URLSearchParams();
    if (q) params.set('q', q);
    if (category) params.set('category', category);
    if (format) params.set('format', format);
    Object.entries(filters).forEach(([key,value])=>{if(value)params.set(key,value)});
    params.set('limit','100');if(offset)params.set('offset',String(offset));
    return fetchJSON<{ packs: Array<{pack:MarketplacePack;latest_compatible?:MarketplacePack['releases'][number];installed?:import('../types/pack').MarketplaceInstalled;modified?:boolean}>; total: number; limit:number;offset:number;generated: string }>(`/marketplace/packs?${params}`).then(data=>({...data,packs:data.packs.map(item=>({...item.pack,latest_compatible:item.latest_compatible,installed:item.installed,modified:item.modified}))}));
  },
  marketplaceStatus: () => fetchJSON<{online:boolean;catalog_available:boolean;catalog_source:string;generated?:string;error?:string;last_refresh?:string;last_refresh_error?:string}>('/marketplace/status'),
  refreshMarketplace: () => fetchJSON<{status:string;generated:string;packs:number}>('/marketplace/refresh',{method:'POST'}),
  installMarketplacePack: (name: string, version = 'latest') => fetchJSON<{ status: string; name: string; version: string; activated?: boolean }>(
    '/packs/install', { method: 'POST', body: JSON.stringify({ name, version }) }
  ),
  upgradeMarketplacePack: (name:string, all=false) => fetchJSON<{results:Array<{name:string;status:string;status_code?:number;version?:string;error?:string}>}>('/packs/upgrade',{method:'POST',body:JSON.stringify({name,all})}),
  removeMarketplacePack: (name:string) => fetchJSON<{status:string;name:string}>(`/packs/${encodeURIComponent(name)}`,{method:'DELETE'}),

  validatePack: (yamlContent: string, sampleLog: string) =>
    fetchJSON<PackValidationResponse>('/packs/validate', {
      method: 'POST',
      body: JSON.stringify({ yaml_content: yamlContent, sample_log: sampleLog }),
    }),

  activatePack: (yamlContent: string, filename?: string) =>
    fetchJSON<{ status: string; name: string; version: string }>('/packs/activate', {
      method: 'POST',
      body: JSON.stringify({ yaml_content: yamlContent, filename }),
    }),

  rollbackPack: (name?:string) =>
    fetchJSON<{ status: string; version: string }>('/packs/rollback', {method: 'POST',body:JSON.stringify(name?{name}:{})}),

  getConfig: () => fetchJSON<RuntimeConfig>('/config'),

  listConnections: () => fetchJSON<{ connections: ConnectionSummary[] }>('/connections'),
  getConnection: (name: string) => fetchText(`/connections/${encodeURIComponent(name)}`),
  validateConnection: (yamlContent: string) =>
    fetchJSON<ConnectionValidationResponse>('/connections/validate', {
      method: 'POST',
      body: JSON.stringify({ yaml_content: yamlContent }),
    }),
  testConnection: (yamlContent: string) =>
    fetchJSON<ConnectionTestResponse>('/connections/test', {
      method: 'POST',
      body: JSON.stringify({ yaml_content: yamlContent }),
    }),
  applyConnection: (yamlContent: string) =>
    fetchJSON<ConnectionApplyResponse>('/connections/apply', {
      method: 'POST',
      body: JSON.stringify({ yaml_content: yamlContent }),
    }),
  rollbackConnection: () =>
    fetchJSON<{ status: string; message: string }>('/connections/rollback', {
      method: 'POST',
    }),
};
