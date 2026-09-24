import { SystemStats, HealthStatus } from '../types/telemetry';
import { NormalizedEvent, EventTrace, QuarantineEntry, SourceSummary } from '../types/event';
import { ParserPackSummary, PackValidationResponse } from '../types/pack';
import { RuntimeConfig } from '../types/config';
import { ConnectionSummary, ConnectionValidationResponse, ConnectionTestResponse, ConnectionApplyResponse } from '../types/connection';

const BASE_URL = '/api/v1';

async function fetchJSON<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${endpoint}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
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

  rollbackPack: () =>
    fetchJSON<{ status: string; version: string }>('/packs/rollback', {
      method: 'POST',
    }),

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

