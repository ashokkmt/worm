export interface LossAudit {
  valid: boolean;
  reason: string;
}

export interface SystemStats {
  eps: number;
  accepted: number;
  normalized: number;
  quarantined: number;
  pending: number;
  delivered: number;
  loss_audit: LossAudit;
  buffer_fill_pct: number;
  unmapped_ratio: number;
  memory_bytes: number;
  goroutines: number;
  uptime_seconds: number;
}

export interface HealthStatus {
  status: string;
  version: string;
  air_gapped: boolean;
  uptime_seconds: number;
  sqlite_status: string;
}
