export interface ConnectionSummary {
  name: string;
  version: string;
  type: string;
  enabled: boolean;
  target: string;
}

export interface ConnectionValidationResponse {
  valid: boolean;
  name?: string;
  version?: string;
  type?: string;
  error?: string;
}

export interface ConnectionTestResponse {
  status: string;
  target: string;
  message: string;
}

export interface ConnectionApplyResponse {
  status: string;
  name: string;
  version: string;
  restart_required?: boolean;
}
