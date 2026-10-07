import React, { useEffect, useState } from 'react';
import { Layers, RefreshCw, CheckCircle2 } from 'lucide-react';
import { MarketplacePack, ParserPackSummary } from '../types/pack';
import { apiService } from '../services/api';
import { Badge } from '../components/Badge';

export const Packs: React.FC = () => {
  const [packs, setPacks] = useState<ParserPackSummary[]>([]);
  const [snapshotVersion, setSnapshotVersion] = useState('');
  const [loading, setLoading] = useState(false);
  const [view, setView] = useState<'installed' | 'marketplace'>('installed');
  const [catalog, setCatalog] = useState<MarketplacePack[]>([]);
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState('');
  const [format, setFormat] = useState('');
  const [vendor,setVendor]=useState('');
  const [product,setProduct]=useState('');
  const [model,setModel]=useState('');
  const [marketError, setMarketError] = useState('');
  const [installing, setInstalling] = useState('');
  const [catalogOffset, setCatalogOffset] = useState(0);
  const [catalogTotal, setCatalogTotal] = useState(0);
  const [marketStatus, setMarketStatus] = useState<{online:boolean;catalog_available:boolean;catalog_source:string;generated?:string;error?:string;last_refresh?:string;last_refresh_error?:string}>();
  const [pageError,setPageError]=useState('');
  const [selectedVersions,setSelectedVersions]=useState<Record<string,string>>({});

  const fetchPacks = async () => {
    setLoading(true);
    try {
      const data = await apiService.listPacks();
      setPacks(data.packs);
      setSnapshotVersion(data.version);
    } catch (e) {
      setPageError(e instanceof Error ? e.message : 'Failed to load installed packs');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchPacks();
    apiService.marketplaceStatus().then(setMarketStatus).catch(e=>setMarketStatus({online:false,catalog_available:false,catalog_source:'unavailable',error:e instanceof Error?e.message:'Marketplace unavailable'}));
  }, []);

  const fetchMarketplace = async (q = query, selectedCategory = category, selectedFormat = format, offset=catalogOffset) => {
    setLoading(true);
    try {
      const data = await apiService.listMarketplace(q, selectedCategory, selectedFormat, offset,{vendor,product,model});
      setCatalog(data.packs);
      setCatalogTotal(data.total);
      setMarketError('');
    } catch (e) {
      setMarketError(e instanceof Error ? e.message : 'Marketplace is unavailable');
    } finally {
      setLoading(false);
    }
  };

  const install = async (pack: MarketplacePack, targetVersion=pack.latest_compatible?.version||'latest') => {
    setInstalling(pack.name);
    try {
      const major=(value:string)=>value.replace(/^v/,'').split('.')[0];
      if(pack.installed && targetVersion===pack.latest_compatible?.version && pack.installed.version!==targetVersion && major(pack.installed.version)===major(targetVersion)){
        const result=await apiService.upgradeMarketplacePack(pack.name);
        const failure=result.results.find(item=>item.status==='error'||(item.status_code??0)>=300);
        if(failure) throw new Error(failure.error||'Upgrade failed');
      } else await apiService.installMarketplacePack(pack.name,targetVersion);
      await fetchPacks();
      setMarketError('');
    } catch (e) {
      setMarketError(e instanceof Error ? e.message : 'Install failed');
    } finally {
      setInstalling('');
    }
  };

  const refreshCatalog=async()=>{setLoading(true);try{await apiService.refreshMarketplace();await fetchMarketplace(query,category,format,0);setCatalogOffset(0);setMarketStatus(await apiService.marketplaceStatus());}catch(e){setMarketError(e instanceof Error?e.message:'Marketplace refresh failed');}finally{setLoading(false)}};

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <h1 className="text-sm font-mono font-bold text-[#f0f6fc] flex items-center">
            <Layers className="w-4 h-4 mr-2 text-[#58a6ff]" />
            Parser Pack Marketplace
          </h1>
          <p className="text-xs font-mono text-[#8b949e] mt-1">
            Browse verified parser packs and manage the versions active in this WORM instance.
          </p>
        </div>
        <div className="flex items-center space-x-3">
          <Badge variant="green">
            <CheckCircle2 className="w-3 h-3 mr-1" />
            Snapshot: {snapshotVersion || 'live'}
          </Badge>
          <button
            onClick={view==='marketplace'?refreshCatalog:fetchPacks}
            className="p-1.5 rounded bg-[#21262d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d]"
            title="Refresh"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
          </button>
        </div>
      </div>

      <div className="flex gap-2" role="tablist" aria-label="Parser packs">
        <button role="tab" aria-selected={view === 'installed'} onClick={() => setView('installed')} className="px-3 py-2 text-xs font-mono border border-[#30363d] rounded">Installed</button>
        <button role="tab" aria-selected={view === 'marketplace'} onClick={() => { setView('marketplace'); void fetchMarketplace(); }} className="px-3 py-2 text-xs font-mono border border-[#30363d] rounded">Marketplace</button>
      </div>

      {view === 'marketplace' && (
        <div className="space-y-4">
          <p className="text-xs font-mono text-[#8b949e]" role="status">Marketplace {marketStatus?.online?'online-enabled':'offline'} · catalog {marketStatus?.catalog_source||'loading'}{marketStatus?.generated?` · generated ${marketStatus.generated}`:''}{marketStatus?.last_refresh_error?` · last refresh failed: ${marketStatus.last_refresh_error}`:''}</p>
          <form className="flex flex-wrap gap-2" onSubmit={(event) => { event.preventDefault(); setCatalogOffset(0); void fetchMarketplace(query, category, format,0); }}>
            <input aria-label="Search packs" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search vendor, model, format, or pack" className="flex-1 bg-[#0d1117] border border-[#30363d] rounded px-3 py-2 text-xs font-mono" />
            <select aria-label="Filter by category" value={category} onChange={(event) => setCategory(event.target.value)} className="bg-[#0d1117] border border-[#30363d] rounded px-2 py-2 text-xs font-mono"><option value="">All categories</option>{['network_device','server','os','endpoint','application','database','cloud','container','iam','iot','other'].map((item) => <option key={item} value={item}>{item}</option>)}</select>
            <select aria-label="Filter by format" value={format} onChange={(event) => setFormat(event.target.value)} className="bg-[#0d1117] border border-[#30363d] rounded px-2 py-2 text-xs font-mono"><option value="">All formats</option>{['syslog','json','csv','cef','xml','leef','text'].map((item) => <option key={item} value={item}>{item.toUpperCase()}</option>)}</select>
            <input aria-label="Filter by vendor" value={vendor} onChange={e=>setVendor(e.target.value)} placeholder="Vendor" className="bg-[#0d1117] border border-[#30363d] rounded px-2 py-2 text-xs font-mono" />
            <input aria-label="Filter by product" value={product} onChange={e=>setProduct(e.target.value)} placeholder="Product" className="bg-[#0d1117] border border-[#30363d] rounded px-2 py-2 text-xs font-mono" />
            <input aria-label="Filter by model" value={model} onChange={e=>setModel(e.target.value)} placeholder="Model" className="bg-[#0d1117] border border-[#30363d] rounded px-2 py-2 text-xs font-mono" />
            <button className="px-3 py-2 text-xs font-mono bg-[#21262d] border border-[#30363d] rounded" type="submit">Search</button>
          </form>
          {marketError && <p role="alert" className="text-xs text-red-400">{marketError}</p>}
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {catalog.map((p) => {
              const release = p.latest_compatible;
              const targetVersion=selectedVersions[p.name]||release?.version||'latest';
              const installedPack = packs.find((item) => item.name === p.name);
              const installed = p.installed?.version === release?.version || installedPack?.version === release?.version;
              return <article key={p.name} className="bg-[#161b22] border border-[#30363d] rounded-md p-4 space-y-3">
                <div className="flex justify-between gap-2"><strong className="text-xs font-mono text-[#58a6ff]">{p.name}</strong><Badge variant="blue">v{release?.version ?? 'no compatible release'}</Badge></div>
                <p className="text-xs text-[#8b949e]">{p.description || 'No description provided.'}</p>
                <div className="text-xs font-mono text-[#8b949e]">{p.category} · {p.format.toUpperCase()} {p.vendor && `· ${p.vendor}`} · {p.publisher}</div>
                {release?.changelog&&<p className="text-xs text-[#8b949e]">{release.changelog}</p>}
                {p.installed&&<div className="text-xs text-[#8b949e]">Installed v{p.installed.version} · {p.installed.origin}{p.installed.pinned?' · pinned':''}</div>}
                {p.modified&&<div className="text-xs text-red-400">Installed file differs from the verified artifact.</div>}
                <label className="block text-xs text-[#8b949e]">Version <select aria-label={`Version for ${p.name}`} value={targetVersion} onChange={e=>setSelectedVersions({...selectedVersions,[p.name]:e.target.value})} className="ml-2 bg-[#0d1117] border border-[#30363d] rounded px-2 py-1">{release&&<option value={release.version}>Latest compatible · {release.version}</option>}{p.releases.filter(item=>!item.withdrawn&&item.version!==release?.version).map(item=><option key={item.version} value={item.version}>{item.version}{item.min_worm?` · requires WORM ${item.min_worm}`:''}</option>)}</select></label>
                <details className="text-xs text-[#8b949e]"><summary>Release history</summary><ul>{p.releases.map(item=><li key={item.version}>v{item.version}{item.withdrawn?' · withdrawn':''}{item.changelog?` · ${item.changelog}`:''}</li>)}</ul></details>
                <button disabled={!targetVersion || (installed&&targetVersion===p.installed?.version) || p.installed?.pinned || p.modified || installing === p.name} onClick={() => void install(p,targetVersion)} className="px-3 py-2 text-xs font-mono border border-[#30363d] rounded disabled:opacity-50">
                  {installed&&targetVersion===p.installed?.version ? 'Installed' : p.installed?.pinned ? 'Pinned' : p.modified ? 'Resolve local changes' : installing === p.name ? (installedPack ? 'Updating…' : 'Installing…') : installedPack ? 'Install selected version' : 'Install'}
                </button>
                {p.installed&&<button onClick={async()=>{if(!window.confirm(`Remove ${p.name}? Logs without a matching parser may be quarantined.`))return;try{await apiService.removeMarketplacePack(p.name);await fetchPacks();await fetchMarketplace()}catch(e){setMarketError(e instanceof Error?e.message:'Remove failed')}}} className="ml-2 px-3 py-2 text-xs font-mono border border-[#30363d] rounded">Remove</button>}
              </article>;
            })}
          </div>
          <div className="flex justify-between"><button disabled={catalogOffset===0} onClick={()=>{const next=Math.max(0,catalogOffset-100);setCatalogOffset(next);void fetchMarketplace(query,category,format,next)}}>Previous</button><span className="text-xs">{catalogTotal?catalogOffset+1:0}–{Math.min(catalogOffset+catalog.length,catalogTotal)} of {catalogTotal}</span><button disabled={catalogOffset+catalog.length>=catalogTotal} onClick={()=>{const next=catalogOffset+100;setCatalogOffset(next);void fetchMarketplace(query,category,format,next)}}>Next</button></div>
        </div>
      )}

      {/* Packs Grid */}
      {view === 'installed' && <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
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
                <div className="flex items-center justify-between"><span className="text-[#8b949e]">Origin:</span><span>{p.origin||'unmanaged'}</span></div>
                {p.pinned&&<div className="text-amber-300">Pinned version</div>}
                {p.modified&&<div className="text-red-400">Modified on disk</div>}
                <button onClick={async()=>{try{await apiService.rollbackPack(p.name);await fetchPacks();setPageError('')}catch(e){setPageError(e instanceof Error?e.message:'Pack rollback failed')}}} className="text-xs px-2 py-1 border border-[#30363d] rounded">Rollback this pack</button>
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
      </div>}
      {view==='installed'&&<button onClick={async()=>{try{await apiService.rollbackPack();await fetchPacks();setPageError('')}catch(e){setPageError(e instanceof Error?e.message:'Rollback failed')}}} className="px-3 py-2 text-xs font-mono border border-[#30363d] rounded">Rollback last pack change</button>}
      {pageError&&<p role="alert" className="text-xs text-red-400">{pageError}</p>}
    </div>
  );
};
