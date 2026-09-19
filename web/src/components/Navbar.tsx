import React from 'react';
import { 
  Activity, 
  Terminal, 
  AlertTriangle, 
  FileCode2, 
  Layers, 
  Settings, 
  ShieldCheck, 
  Lock 
} from 'lucide-react';
import { Badge } from './Badge';

interface NavbarProps {
  currentTab: string;
  onSelectTab: (tab: string) => void;
  eps?: number;
  invariantValid?: boolean;
}

export const Navbar: React.FC<NavbarProps> = ({
  currentTab,
  onSelectTab,
  eps = 0,
  invariantValid = true,
}) => {
  const tabs = [
    { id: 'dashboard', label: 'Dashboard', icon: <Activity className="w-4 h-4 mr-1.5" /> },
    { id: 'events', label: 'Events Explorer', icon: <Terminal className="w-4 h-4 mr-1.5" /> },
    { id: 'quarantine', label: 'Quarantine DLQ', icon: <AlertTriangle className="w-4 h-4 mr-1.5" /> },
    { id: 'onboard', label: 'Pack Studio', icon: <FileCode2 className="w-4 h-4 mr-1.5" /> },
    { id: 'packs', label: 'Parser Packs', icon: <Layers className="w-4 h-4 mr-1.5" /> },
    { id: 'config', label: 'System Config', icon: <Settings className="w-4 h-4 mr-1.5" /> },
  ];

  return (
    <header className="bg-[#161b22] border-b border-[#30363d] sticky top-0 z-50">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="flex items-center justify-between h-14">
          
          {/* Logo & Brand */}
          <div className="flex items-center space-x-3 cursor-pointer" onClick={() => onSelectTab('dashboard')}>
            <div className="bg-[#1f6feb] text-white px-2 py-0.5 rounded font-mono font-bold text-sm tracking-wider flex items-center">
              WORM
            </div>
            <span className="text-xs font-mono text-[#8b949e] hidden md:inline">
              SIH 26156 // Universal Log Pre-processing
            </span>
          </div>

          {/* Navigation Links */}
          <nav className="hidden lg:flex space-x-1">
            {tabs.map((tab) => {
              const active = currentTab === tab.id;
              return (
                <button
                  key={tab.id}
                  onClick={() => onSelectTab(tab.id)}
                  className={`flex items-center px-3 py-1.5 rounded-md text-xs font-mono font-medium transition-colors ${
                    active
                      ? 'bg-[#21262d] text-[#58a6ff] border border-[#388bfd]/40'
                      : 'text-[#8b949e] hover:text-[#c9d1d9] hover:bg-[#21262d]/50 border border-transparent'
                  }`}
                >
                  {tab.icon}
                  {tab.label}
                </button>
              );
            })}
          </nav>

          {/* Operational Badges */}
          <div className="flex items-center space-x-3">
            {/* Live EPS */}
            <div className="flex items-center font-mono text-xs text-[#8b949e] bg-[#0d1117] px-2.5 py-1 rounded border border-[#30363d]">
              <span className="w-2 h-2 rounded-full bg-emerald-500 mr-2 animate-pulse" />
              <span className="font-semibold text-[#c9d1d9] mr-1">{eps.toFixed(1)}</span>
              <span>EPS</span>
            </div>

            {/* Invariant status */}
            <div className="hidden sm:block">
              {invariantValid ? (
                <Badge variant="green">
                  <ShieldCheck className="w-3 h-3 mr-1" />
                  0-LOSS VALID
                </Badge>
              ) : (
                <Badge variant="red">
                  <AlertTriangle className="w-3 h-3 mr-1" />
                  INVARIANT BREACH
                </Badge>
              )}
            </div>

            {/* Air-Gapped Strict */}
            <Badge variant="purple">
              <Lock className="w-3 h-3 mr-1" />
              AIR-GAPPED
            </Badge>
          </div>

        </div>

        {/* Mobile Tab Nav */}
        <div className="flex lg:hidden overflow-x-auto py-2 border-t border-[#21262d] space-x-1">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              onClick={() => onSelectTab(tab.id)}
              className={`flex items-center px-2.5 py-1 whitespace-nowrap rounded text-xs font-mono ${
                currentTab === tab.id
                  ? 'bg-[#21262d] text-[#58a6ff]'
                  : 'text-[#8b949e] hover:text-[#c9d1d9]'
              }`}
            >
              {tab.label}
            </button>
          ))}
        </div>

      </div>
    </header>
  );
};
