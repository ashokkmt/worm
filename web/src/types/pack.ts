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
  origin?: string;
  pinned?: boolean;
  digest?: string;
  filename?: string;
  modified?: boolean;
}

export interface MarketplaceRelease {
  version: string;
  artifact: string;
  sha256: string;
  size: number;
  pack_api: string;
  ocsf: string;
  withdrawn?: boolean;
  changelog?: string;
  min_worm?: string;
}

export interface MarketplaceInstalled {
  name: string;
  filename: string;
  version: string;
  digest: string;
  origin: string;
  pinned: boolean;
  modified?: boolean;
}

export interface MarketplacePack {
  name: string;
  description: string;
  publisher: string;
  category: string;
  format: string;
  vendor?: string;
  product?: string;
  models?: string[];
  tags?: string[];
  releases: MarketplaceRelease[];
  latest_compatible?: MarketplaceRelease;
  installed?: MarketplaceInstalled;
  modified?: boolean;
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
