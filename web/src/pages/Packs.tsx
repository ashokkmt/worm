import React, { useEffect, useState } from 'react';
import { Layers, RefreshCw, CheckCircle2 } from 'lucide-react';
import { ParserPackSummary } from '../types/pack';
import { apiService } from '../services/api';
import { Badge } from '../components/Badge';

export const Packs: React.FC = () => {
  const [packs, setPacks] = useState<ParserPackSummary[]>([]);
  const [snapshotVersion, setSnapshotVersion] = useState('');
  const [loading, setLoading] = useState(false);

  const fetchPacks = async () => {
    setLoading(true);
    try {
      const data = await apiService.listPacks();
      setPacks(data.packs);
      setSnapshotVersion(data.version);
    } catch (e) {
      console.error('Failed to load parser packs', e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchPacks();
  }, []);

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <h1 className="text-sm font-mono font-bold text-[#f0f6fc] flex items-center">
            <Layers className="w-4 h-4 mr-2 text-[#58a6ff]" />
            Active Parser Packs Inventory
          </h1>
          <p className="text-xs font-mono text-[#8b949e] mt-1">
            Declarative source parsers currently loaded in the active pipeline snapshot.
          </p>
        </div>
        <div className="flex items-center space-x-3">
          <Badge variant="green">
            <CheckCircle2 className="w-3 h-3 mr-1" />
            Snapshot: {snapshotVersion || 'live'}
          </Badge>
          <button
            onClick={fetchPacks}
            className="p-1.5 rounded bg-[#21262d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d]"
            title="Refresh"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
          </button>
        </div>
      </div>

      {/* Packs Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {packs.map((p) => (
          <div
            key={p.name}
            className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col justify-between space-y-3"
          >
            <div>
              <div className="flex items-center justify-between pb-2 border-b border-[#21262d]">
                <span className="font-mono font-bold text-xs text-[#58a6ff]">
                  {p.name}
                </span>
                <Badge variant="blue">v{p.version}</Badge>
              </div>

              <p className="text-xs font-mono text-[#8b949e] mt-2 line-clamp-2">
                {p.description || 'No description provided.'}
              </p>

              <div className="mt-3 space-y-1.5 text-xs font-mono">
                <div className="flex items-center justify-between">
                  <span className="text-[#8b949e]">Category:</span>
                  <Badge variant="purple">{p.source_category}</Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-[#8b949e]">Format:</span>
                  <Badge variant="gray">{p.format.toUpperCase()}</Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-[#8b949e]">Extracted Fields:</span>
                  <span className="text-[#f0f6fc] font-semibold">{p.field_count} rules</span>
                </div>
                {p.author && (
                  <div className="flex items-center justify-between">
                    <span className="text-[#8b949e]">Author:</span>
                    <span className="text-[#c9d1d9]">{p.author}</span>
                  </div>
                )}
              </div>
            </div>

            {/* Match Rule Summary */}
            <div className="bg-[#0d1117] border border-[#21262d] rounded p-2 text-[11px] font-mono">
              <div className="text-[#8b949e] mb-0.5 font-semibold">Match Signature:</div>
              <div className="text-emerald-400 truncate">
                {p.match?.contains && `contains: "${p.match.contains}"`}
                {p.match?.regex && `regex: "${p.match.regex}"`}
                {p.match?.fieldEquals && `fieldEquals: ${JSON.stringify(p.match.fieldEquals)}`}
                {!p.match?.contains && !p.match?.regex && !p.match?.fieldEquals && 'All incoming records'}
              </div>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
};
