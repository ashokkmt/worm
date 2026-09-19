import React, { useState, useEffect } from 'react';
import { Navbar } from './components/Navbar';
import { Dashboard } from './pages/Dashboard';
import { Events } from './pages/Events';
import { Quarantine } from './pages/Quarantine';
import { Onboard } from './pages/Onboard';
import { Packs } from './pages/Packs';
import { Config } from './pages/Config';
import { apiService } from './services/api';
import { SystemStats } from './types/telemetry';

export const App: React.FC = () => {
  const [currentTab, setCurrentTab] = useState<string>('dashboard');
  const [navParam, setNavParam] = useState<string | undefined>();
  const [stats, setStats] = useState<SystemStats | null>(null);
  const [statsError, setStatsError] = useState<boolean>(false);

  const fetchStats = async () => {
    try {
      const data = await apiService.getStats();
      setStats(data);
      setStatsError(false);
    } catch {
      setStatsError(true);
    }
  };

  useEffect(() => {
    fetchStats();
    const interval = setInterval(fetchStats, 2000);
    return () => clearInterval(interval);
  }, []);

  const handleNavigate = (tab: string, param?: string) => {
    setCurrentTab(tab);
    setNavParam(param);
    window.scrollTo({ top: 0, behavior: 'smooth' });
  };

  return (
    <div className="min-h-screen bg-[#0d1117] text-[#c9d1d9] flex flex-col font-sans">
      <Navbar
        currentTab={currentTab}
        onSelectTab={(tab) => handleNavigate(tab)}
        eps={stats?.eps || 0}
        invariantValid={statsError ? null : (stats?.loss_audit?.valid ?? null)}
      />

      <main className="flex-1 max-w-7xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-6">
        {currentTab === 'dashboard' && (
          <Dashboard onNavigate={handleNavigate} stats={stats} onRefreshStats={fetchStats} />
        )}
        {currentTab === 'events' && <Events initialEventId={navParam} />}
        {currentTab === 'quarantine' && <Quarantine />}
        {currentTab === 'onboard' && <Onboard />}
        {currentTab === 'packs' && <Packs />}
        {currentTab === 'config' && <Config />}
      </main>

      <footer className="bg-[#161b22] border-t border-[#30363d] py-3 text-center text-xs font-mono text-[#8b949e]">
        <div className="max-w-7xl mx-auto px-4 flex flex-col sm:flex-row items-center justify-between gap-2">
          <span>WORM // SIH 26156 &mdash; Universal Log Pre-processing Framework</span>
          <span className="text-emerald-400">Air-Gapped Operational Console &bull; Zero External Dependencies</span>
        </div>
      </footer>
    </div>
  );
};

export default App;
