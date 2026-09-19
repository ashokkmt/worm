export interface MatchRule {
  contains?: string;
  regex?: string;
  fieldEquals?: Record<string, string>;
}

export interface ParserPackSummary {
  name: string;
  version: string;
  description: string;
  author: string;
  source_category: string;
  format: string;
  match: MatchRule;
  field_count: number;
}

export interface PackValidationResponse {
  valid: boolean;
  name?: string;
  version?: string;
  source_category?: string;
  format?: string;
  match?: boolean;
  match_error?: string;
  extracted_fields?: Record<string, any>;
  error?: string;
}
