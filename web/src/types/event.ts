export interface ProcessingStep {
  stage: string;
  timestamp: string;
  result: string;
  error?: string;
}

export interface WormEnvelope {
  event_id: string;
  raw_id: string;
  raw_sha256: string;
  raw_bytes: number;
  record_ordinal: number;
  source_category: string;
  source_id: string;
  parser_pack: string;
  parser_version: string;
  core_version: string;
  schema_version: string;
  received_time: string;
  status: string;
  warnings: string[];
  processing_history: ProcessingStep[];
}

export interface NormalizedEvent {
  worm: WormEnvelope;
  ocsf: Record<string, any>;
}

export interface EventTrace {
  event_id: string;
  raw_id: string;
  raw_sha256: string;
  raw_payload: string;
  raw_hex: string;
  byte_count: number;
  transport: string;
  source_ip: string;
  received_at: string;
  processing_history: ProcessingStep[];
  unmapped: any;
  ocsf: Record<string, any>;
}

export interface QuarantineEntry {
  quarantine_id: string;
  raw_id: string;
  raw_sha256: string;
  stage: string;
  reason: string;
  error_details: string;
  raw_preview: string;
  candidate_packs?: string[];
  replay_eligible: boolean;
  quarantined_at: string;
  replayed_at?: string;
}

export interface SourceSummary {
  category: string;
  source_id: string;
  events: number;
  last_seen: string;
}
