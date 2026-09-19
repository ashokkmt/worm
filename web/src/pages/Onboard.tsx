import React, { useState } from 'react';
import { Play, CheckCircle2, AlertTriangle, RotateCcw, Upload, FileCode } from 'lucide-react';
import { apiService } from '../services/api';
import { PackValidationResponse } from '../types/pack';
import { Badge } from '../components/Badge';

const TEMPLATES: Record<string, { yaml: string; sample: string }> = {
  custom_firewall: {
    yaml: `apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: firewall-paloalto-threat
  version: 1.0.0
  description: Palo Alto Networks Threat Log Stream
  author: SecOps
spec:
  sourceCategory: network_device
  format: csv
  match:
    contains: "THREAT"
  fields:
    src_ip:
      from: field_7
      type: ip
    dest_ip:
      from: field_8
      type: ip
    rule_name:
      from: field_11
      type: string
  map:
    src_ip: src_endpoint.ip
    dest_ip: dst_endpoint.ip
  preserveUnmapped: true
`,
    sample: `1,2026/09/18 10:15:30,0012345678,THREAT,vulnerability,1,2026/09/18 10:15:30,192.168.10.50,10.0.0.1,0.0.0.0,0.0.0.0,allow-dns,trust,untrust,ethernet1/1,ethernet1/2,default-log,2026/09/18 10:15:30,12345,1,53,53,0,0,0x0,udp,alert,"",999999,any,informational,client-to-server,0,0x0,192.168.0.0-192.168.255.255,10.0.0.0-10.255.255.255,0,,0,,,0,,,,,,,,0,0,0,0,0`,
  },
  custom_json_app: {
    yaml: `apiVersion: worm.io/v1
kind: LogSource
metadata:
  name: app-k8s-microservice
  version: 1.0.0
  description: Cloud-native JSON application event
  author: PlatformTeam
spec:
  sourceCategory: application
  format: json
  match:
    fieldEquals:
      app_name: checkout-service
  fields:
    customer_id:
      from: user_id
      type: string
    status_code:
      from: status
      type: integer
    duration_ms:
      from: latency
      type: float
  map:
    customer_id: user.uid
    status_code: http_response.code
  preserveUnmapped: true
`,
    sample: `{"app_name":"checkout-service","user_id":"usr_9981","status":200,"latency":42.5,"message":"order placed successfully"}`,
  },
};

export const Onboard: React.FC = () => {
  const [yamlContent, setYamlContent] = useState(TEMPLATES.custom_firewall.yaml);
  const [sampleLog, setSampleLog] = useState(TEMPLATES.custom_firewall.sample);
  const [validationResult, setValidationResult] = useState<PackValidationResponse | null>(null);
  const [validating, setValidating] = useState(false);
  const [activating, setActivating] = useState(false);
  const [feedback, setFeedback] = useState<{ type: 'success' | 'error'; message: string } | null>(null);

  const handleSelectTemplate = (key: string) => {
    if (TEMPLATES[key]) {
      setYamlContent(TEMPLATES[key].yaml);
      setSampleLog(TEMPLATES[key].sample);
      setValidationResult(null);
      setFeedback(null);
    }
  };

  const handleValidate = async () => {
    setValidating(true);
    setFeedback(null);
    try {
      const res = await apiService.validatePack(yamlContent, sampleLog);
      setValidationResult(res);
      if (res.valid && res.match) {
        setFeedback({ type: 'success', message: 'Parser pack validated and successfully matched sample record!' });
      } else if (res.valid && !res.match) {
        setFeedback({ type: 'error', message: 'YAML schema valid, but match rule did NOT match the sample log!' });
      } else {
        setFeedback({ type: 'error', message: res.error || 'Validation error' });
      }
    } catch (e: any) {
      setFeedback({ type: 'error', message: 'Validation request failed: ' + e.message });
    } finally {
      setValidating(false);
    }
  };

  const handleActivate = async () => {
    setActivating(true);
    setFeedback(null);
    try {
      const res = await apiService.activatePack(yamlContent);
      setFeedback({
        type: 'success',
        message: `Successfully activated parser pack "${res.name}" into live immutable pipeline snapshot!`,
      });
    } catch (e: any) {
      setFeedback({ type: 'error', message: 'Activation failed: ' + e.message });
    } finally {
      setActivating(false);
    }
  };

  const handleRollback = async () => {
    if (!confirm('Are you sure you want to rollback to the previous parser pack snapshot?')) return;
    try {
      const res = await apiService.rollbackPack();
      setFeedback({
        type: 'success',
        message: `Rolled back to previous snapshot (${res.version})`,
      });
    } catch (e: any) {
      setFeedback({ type: 'error', message: 'Rollback failed: ' + e.message });
    }
  };

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col md:flex-row md:items-center justify-between gap-3">
        <div>
          <h1 className="text-sm font-mono font-bold text-[#f0f6fc] flex items-center">
            <FileCode className="w-4 h-4 mr-2 text-[#58a6ff]" />
            Source Onboarding &amp; Parser Pack Studio
          </h1>
          <p className="text-xs font-mono text-[#8b949e] mt-1">
            Author declarative YAML parser packs with sandbox evaluation against raw logs, then atomically activate.
          </p>
        </div>

        {/* Template Selector */}
        <div className="flex items-center space-x-2">
          <span className="text-xs font-mono text-[#8b949e]">Preset:</span>
          <select
            onChange={(e) => handleSelectTemplate(e.target.value)}
            className="bg-[#0d1117] border border-[#30363d] rounded text-xs font-mono text-[#c9d1d9] px-2.5 py-1"
          >
            <option value="custom_firewall">Palo Alto Threat (CSV)</option>
            <option value="custom_json_app">K8s App Microservice (JSON)</option>
          </select>
        </div>
      </div>

      {feedback && (
        <div
          className={`p-3 rounded text-xs font-mono border flex items-center ${
            feedback.type === 'success'
              ? 'bg-emerald-950/60 border-emerald-800 text-emerald-300'
              : 'bg-rose-950/60 border-rose-800 text-rose-300'
          }`}
        >
          {feedback.type === 'success' ? (
            <CheckCircle2 className="w-4 h-4 mr-2 shrink-0" />
          ) : (
            <AlertTriangle className="w-4 h-4 mr-2 shrink-0" />
          )}
          {feedback.message}
        </div>
      )}

      {/* Editor & Test Runner Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        
        {/* Left Column: YAML Definition Editor */}
        <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col space-y-3">
          <div className="flex items-center justify-between pb-2 border-b border-[#21262d]">
            <span className="text-xs font-mono uppercase font-bold text-[#f0f6fc]">
              Declarative Parser Pack (YAML)
            </span>
            <Badge variant="blue">worm.io/v1</Badge>
          </div>

          <textarea
            value={yamlContent}
            onChange={(e) => setYamlContent(e.target.value)}
            className="w-full h-96 bg-[#0d1117] border border-[#30363d] rounded p-3 text-xs font-mono text-[#c9d1d9] focus:outline-none focus:border-[#58a6ff] leading-relaxed resize-none"
            spellCheck={false}
          />

          <div className="flex items-center justify-between pt-2">
            <button
              onClick={handleRollback}
              className="px-3 py-1.5 rounded bg-[#21262d] hover:bg-[#30363d] text-xs font-mono text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d] flex items-center"
              title="Rollback to previous pack snapshot"
            >
              <RotateCcw className="w-3.5 h-3.5 mr-1" />
              Rollback Snapshot
            </button>

            <button
              onClick={handleActivate}
              disabled={activating}
              className="px-4 py-1.5 rounded bg-[#238636] hover:bg-[#2ea043] text-white text-xs font-mono font-medium flex items-center disabled:opacity-50 transition shadow-xs"
            >
              <Upload className="w-3.5 h-3.5 mr-1.5" />
              {activating ? 'Activating...' : 'Activate Pack (Live)'}
            </button>
          </div>
        </div>

        {/* Right Column: Sample Log & Test Runner */}
        <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col space-y-3">
          <div className="flex items-center justify-between pb-2 border-b border-[#21262d]">
            <span className="text-xs font-mono uppercase font-bold text-[#f0f6fc]">
              Sample Raw Log Input
            </span>
            <span className="text-xs font-mono text-[#8b949e]">Sandbox Runner</span>
          </div>

          <textarea
            value={sampleLog}
            onChange={(e) => setSampleLog(e.target.value)}
            placeholder="Paste raw log string here..."
            className="w-full h-28 bg-[#0d1117] border border-[#30363d] rounded p-3 text-xs font-mono text-[#c9d1d9] focus:outline-none focus:border-[#58a6ff] leading-relaxed resize-none"
            spellCheck={false}
          />

          <button
            onClick={handleValidate}
            disabled={validating}
            className="w-full py-2 bg-[#1f6feb] hover:bg-[#388bfd] text-white text-xs font-mono font-medium rounded flex items-center justify-center disabled:opacity-50 transition"
          >
            <Play className="w-3.5 h-3.5 mr-1.5" />
            {validating ? 'Running Validation...' : 'Validate Pack & Match Sample'}
          </button>

          {/* Validation & Extracted Fields Preview */}
          <div className="flex-1 bg-[#0d1117] border border-[#21262d] rounded p-3 overflow-auto">
            <div className="text-xs font-mono uppercase tracking-wider text-[#8b949e] mb-2">
              Sandbox Execution Output
            </div>
            {validationResult ? (
              <div className="space-y-2 text-xs font-mono">
                <div className="flex items-center space-x-2">
                  <span className="text-[#8b949e]">Schema Valid:</span>
                  <Badge variant={validationResult.valid ? 'green' : 'red'}>
                    {validationResult.valid ? 'VALID' : 'INVALID'}
                  </Badge>
                </div>
                {validationResult.valid && (
                  <>
                    <div className="flex items-center space-x-2">
                      <span className="text-[#8b949e]">Matched Sample:</span>
                      <Badge variant={validationResult.match ? 'green' : 'amber'}>
                        {validationResult.match ? 'MATCHED' : 'NO MATCH'}
                      </Badge>
                    </div>
                    {validationResult.extracted_fields && (
                      <div>
                        <div className="text-[#8b949e] mt-2 mb-1">Extracted Source Fields:</div>
                        <pre className="bg-[#161b22] border border-[#30363d] p-2 rounded text-emerald-400 overflow-x-auto">
                          {JSON.stringify(validationResult.extracted_fields, null, 2)}
                        </pre>
                      </div>
                    )}
                  </>
                )}
                {validationResult.error && (
                  <div className="text-rose-400 mt-2">{validationResult.error}</div>
                )}
              </div>
            ) : (
              <div className="text-xs font-mono text-[#8b949e] py-8 text-center">
                Click &quot;Validate Pack &amp; Match Sample&quot; to run sandbox evaluation.
              </div>
            )}
          </div>
        </div>

      </div>
    </div>
  );
};
