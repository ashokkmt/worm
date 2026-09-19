import React, { useState } from 'react';
import { ShieldCheck, AlertTriangle, CheckCircle2, ChevronRight, Hash, FileText } from 'lucide-react';
import { EventTrace } from '../types/event';
import { apiService } from '../services/api';
import { Badge } from './Badge';

interface TraceViewerProps {
  trace: EventTrace;
  onClose?: () => void;
}

export const TraceViewer: React.FC<TraceViewerProps> = ({ trace, onClose }) => {
  const [viewMode, setViewMode] = useState<'text' | 'hex'>('text');

  // Raw verification state
  const [verifyingRaw, setVerifyingRaw] = useState(false);
  const [rawVerifyResult, setRawVerifyResult] = useState<{
    matches: boolean;
    computed_sha256: string;
    status: string;
  } | null>(null);

  // Normalized event verification state
  const [verifyingEvent, setVerifyingEvent] = useState(false);
  const [eventVerifyResult, setEventVerifyResult] = useState<{
    passed: boolean;
    status: string;
    stored_hash: string;
    computed_hash: string;
    hash_matches: boolean;
    message_matches: boolean;
    raw_valid: boolean;
    failure_reason?: string;
  } | null>(null);

  const handleVerifyRaw = async () => {
    setVerifyingRaw(true);
    try {
      const res = await apiService.verifyRaw(trace.raw_id);
      setRawVerifyResult({
        matches: res.matches,
        computed_sha256: res.computed_sha256,
        status: res.status,
      });
    } catch (e: any) {
      alert('Raw verification request failed: ' + e.message);
    } finally {
      setVerifyingRaw(false);
    }
  };

  const handleVerifyEvent = async () => {
    setVerifyingEvent(true);
    try {
      const res = await apiService.verifyEvent(trace.event_id);
      setEventVerifyResult({
        passed: res.passed,
        status: res.status,
        stored_hash: res.stored_hash,
        computed_hash: res.computed_hash,
        hash_matches: res.hash_matches,
        message_matches: res.message_matches,
        raw_valid: res.raw_valid,
        failure_reason: res.failure_reason,
      });
    } catch (e: any) {
      alert('Event verification request failed: ' + e.message);
    } finally {
      setVerifyingEvent(false);
    }
  };

  // Format hex dump like hexdump -C
  const renderHexDump = (hexStr: string) => {
    if (!hexStr) return '';
    const bytes: string[] = [];
    for (let i = 0; i < hexStr.length; i += 2) {
      bytes.push(hexStr.substring(i, i + 2));
    }

    const lines: string[] = [];
    for (let i = 0; i < bytes.length; i += 16) {
      const chunk = bytes.slice(i, i + 16);
      const offset = i.toString(16).padStart(8, '0');
      const hexPart = chunk.map((b, idx) => (idx === 8 ? ` ${b}` : b)).join(' ').padEnd(50, ' ');
      const asciiPart = chunk
        .map((b) => {
          const code = parseInt(b, 16);
          return code >= 32 && code <= 126 ? String.fromCharCode(code) : '.';
        })
        .join('');
      lines.push(`${offset}  ${hexPart} |${asciiPart}|`);
    }
    return lines.join('\n');
  };

  return (
    <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 sm:p-5 space-y-6 w-full max-w-full min-w-0 box-border overflow-hidden">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between pb-3 border-b border-[#21262d] gap-2 min-w-0">
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-mono font-bold text-[#f0f6fc] flex items-center min-w-0">
            <Hash className="w-4 h-4 mr-1.5 text-[#58a6ff] shrink-0" />
            <span className="truncate">Forensic Lineage &amp; Verification Trace</span>
          </h3>
          <p className="text-xs font-mono text-[#8b949e] mt-0.5 break-all sm:truncate">
            Event: {trace.event_id} | Raw ID: {trace.raw_id}
          </p>
        </div>
        {onClose && (
          <button
            onClick={onClose}
            className="text-xs font-mono text-[#8b949e] hover:text-[#c9d1d9] px-2.5 py-1 rounded bg-[#21262d] shrink-0 self-start sm:self-auto border border-[#30363d]"
          >
            Close
          </button>
        )}
      </div>

      {/* Stage Timeline Stepper */}
      <div className="min-w-0">
        <div className="text-xs font-mono uppercase tracking-wider text-[#8b949e] mb-3">
          Pipeline Transformation Lifecycle
        </div>
        <div className="flex flex-wrap items-center gap-2 min-w-0">
          {trace.processing_history?.map((step, idx) => (
            <React.Fragment key={idx}>
              <div className="flex items-center space-x-1.5 bg-[#0d1117] border border-[#30363d] px-3 py-1.5 rounded-md text-xs font-mono shrink-0 max-w-full">
                {step.result === 'failed' ? (
                  <AlertTriangle className="w-3.5 h-3.5 text-rose-400 shrink-0" />
                ) : (
                  <CheckCircle2 className="w-3.5 h-3.5 text-emerald-400 shrink-0" />
                )}
                <span className="font-semibold text-[#f0f6fc] truncate">{step.stage}</span>
                <span className="text-[#8b949e] shrink-0">({step.result})</span>
              </div>
              {idx < trace.processing_history.length - 1 && (
                <ChevronRight className="w-3.5 h-3.5 text-[#484f58] shrink-0" />
              )}
            </React.Fragment>
          ))}
        </div>
      </div>

      {/* Dual Cryptographic Verification Section */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 min-w-0">

        {/* 1. Normalized Event Integrity */}
        <div className="bg-[#0d1117] border border-[#30363d] rounded-md p-4 flex flex-col justify-between space-y-3 min-w-0">
          <div className="min-w-0">
            <div className="flex items-center justify-between pb-1 border-b border-[#21262d] gap-2 min-w-0">
              <span className="text-xs font-mono uppercase font-bold text-[#58a6ff] truncate">
                Normalized Event Integrity
              </span>
              <Badge variant="blue">-verify-event</Badge>
            </div>
            <div className="text-[11px] font-mono text-[#8b949e] mt-1.5 truncate" title={trace.event_id}>
              ID: {trace.event_id}
            </div>
          </div>

          <button
            onClick={handleVerifyEvent}
            disabled={verifyingEvent}
            className="w-full py-1.5 bg-[#1f6feb] hover:bg-[#388bfd] text-white text-xs font-mono font-medium rounded transition flex items-center justify-center disabled:opacity-50"
          >
            <ShieldCheck className="w-3.5 h-3.5 mr-1.5 shrink-0" />
            <span className="truncate">{verifyingEvent ? 'Verifying Event...' : 'Verify Normalized Event'}</span>
          </button>

          {eventVerifyResult && (
            <div
              className={`p-2.5 rounded border text-xs font-mono break-words ${eventVerifyResult.passed
                  ? 'bg-emerald-950/60 border-emerald-800 text-emerald-300'
                  : 'bg-rose-950/60 border-rose-800 text-rose-300'
                }`}
            >
              <div className="font-bold break-words">
                {eventVerifyResult.passed
                  ? '✓ PASSED: Event JSON & Message match ground truth.'
                  : `✗ ${eventVerifyResult.status}: ${eventVerifyResult.failure_reason}`}
              </div>
            </div>
          )}
        </div>

        {/* 2. Raw Wire Bytes Integrity */}
        <div className="bg-[#0d1117] border border-[#30363d] rounded-md p-4 flex flex-col justify-between space-y-3 min-w-0">
          <div className="min-w-0">
            <div className="flex items-center justify-between pb-1 border-b border-[#21262d] gap-2 min-w-0">
              <span className="text-xs font-mono uppercase font-bold text-emerald-400 truncate">
                Raw Wire Stream Integrity
              </span>
              <Badge variant="green">-verify</Badge>
            </div>
            <div className="text-[11px] font-mono text-emerald-400 mt-1.5 truncate font-semibold" title={trace.raw_sha256}>
              SHA: {trace.raw_sha256}
            </div>
          </div>

          <button
            onClick={handleVerifyRaw}
            disabled={verifyingRaw}
            className="w-full py-1.5 bg-[#238636] hover:bg-[#2ea043] text-white text-xs font-mono font-medium rounded transition flex items-center justify-center disabled:opacity-50"
          >
            <ShieldCheck className="w-3.5 h-3.5 mr-1.5 shrink-0" />
            <span className="truncate">{verifyingRaw ? 'Verifying Raw...' : 'Verify Raw Wire Bytes'}</span>
          </button>

          {rawVerifyResult && (
            <div
              className={`p-2.5 rounded border text-xs font-mono break-words ${rawVerifyResult.matches
                  ? 'bg-emerald-950/60 border-emerald-800 text-emerald-300'
                  : 'bg-rose-950/60 border-rose-800 text-rose-300'
                }`}
            >
              <div className="font-bold break-words">
                {rawVerifyResult.matches
                  ? '✓ PASSED: Verbatim wire bytes match stored SHA-256.'
                  : '✗ TAMPER DETECTED: Raw payload was altered in database!'}
              </div>
              <div className="text-[10px] opacity-80 mt-0.5 truncate" title={rawVerifyResult.computed_sha256}>
                Computed: {rawVerifyResult.computed_sha256}
              </div>
            </div>
          )}
        </div>

      </div>

      {/* Raw Payload Inspector (Text / Hex) */}
      <div className="min-w-0">
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 mb-2 min-w-0">
          <div className="text-xs font-mono uppercase tracking-wider text-[#8b949e] truncate">
            Raw Byte Ingestion Stream ({trace.byte_count} bytes from {trace.transport}://{trace.source_ip})
          </div>
          <div className="flex space-x-1 bg-[#0d1117] p-0.5 rounded border border-[#30363d] shrink-0 self-start sm:self-auto">
            <button
              onClick={() => setViewMode('text')}
              className={`px-2 py-0.5 text-xs font-mono rounded ${viewMode === 'text' ? 'bg-[#21262d] text-[#58a6ff]' : 'text-[#8b949e]'
                }`}
            >
              <FileText className="w-3 h-3 inline mr-1" />
              Text
            </button>
            <button
              onClick={() => setViewMode('hex')}
              className={`px-2 py-0.5 text-xs font-mono rounded ${viewMode === 'hex' ? 'bg-[#21262d] text-[#58a6ff]' : 'text-[#8b949e]'
                }`}
            >
              <Hash className="w-3 h-3 inline mr-1" />
              Hex Dump
            </button>
          </div>
        </div>

        <pre className="bg-[#0d1117] border border-[#21262d] p-3 rounded-md text-xs font-mono text-[#c9d1d9] overflow-x-auto max-h-60 leading-relaxed w-full max-w-full min-w-0">
          {viewMode === 'text' ? trace.raw_payload : renderHexDump(trace.raw_hex)}
        </pre>
      </div>

      {/* Unmapped Fields (Zero-Loss Preservation) */}
      {trace.unmapped && Object.keys(trace.unmapped).length > 0 && (
        <div className="min-w-0">
          <div className="flex items-center space-x-2 mb-2 min-w-0">
            <div className="text-xs font-mono uppercase tracking-wider text-amber-400 truncate">
              Preserved Unmapped Fields (Lossless Storage)
            </div>
            <Badge variant="amber">
              {Object.keys(trace.unmapped).length} fields
            </Badge>
          </div>
          <pre className="bg-[#0d1117] border border-amber-900/50 p-3 rounded-md text-xs font-mono text-amber-200/90 overflow-x-auto max-h-48 w-full max-w-full min-w-0">
            {JSON.stringify(trace.unmapped, null, 2)}
          </pre>
        </div>
      )}

      {/* Normalized OCSF Document Preview */}
      <div className="min-w-0">
        <div className="text-xs font-mono uppercase tracking-wider text-[#8b949e] mb-2">
          Normalized OCSF Document
        </div>
        <pre className="bg-[#0d1117] border border-[#21262d] p-3 rounded-md text-xs font-mono text-[#c9d1d9] overflow-x-auto max-h-64 w-full max-w-full min-w-0">
          {JSON.stringify(trace.ocsf, null, 2)}
        </pre>
      </div>
    </div>
  );
};
