import React, { useState } from 'react';
import {
  Activity,
  Terminal,
  AlertTriangle,
  FileCode2,
  Layers,
  Settings,
  ShieldCheck,
  Lock,
  Menu,
  X
} from 'lucide-react';
import { Badge } from './Badge';

interface NavbarProps {
  currentTab: string;
  onSelectTab: (tab: string) => void;
  eps?: number;
  invariantValid?: boolean | null;
}

export const Navbar: React.FC<NavbarProps> = ({
  currentTab,
  onSelectTab,
  eps = 0,
  invariantValid = null,
}) => {
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  const tabs = [
    { id: 'dashboard', label: 'Dashboard', icon: <Activity className="w-4 h-4 mr-1.5" /> },
    { id: 'events', label: 'Events Explorer', icon: <Terminal className="w-4 h-4 mr-1.5" /> },
    { id: 'quarantine', label: 'Quarantine DLQ', icon: <AlertTriangle className="w-4 h-4 mr-1.5" /> },
    { id: 'onboard', label: 'Pack Studio', icon: <FileCode2 className="w-4 h-4 mr-1.5" /> },
    { id: 'packs', label: 'Parser Packs', icon: <Layers className="w-4 h-4 mr-1.5" /> },
    { id: 'config', label: 'System Config', icon: <Settings className="w-4 h-4 mr-1.5" /> },
  ];

  const handleMobileTabClick = (tabId: string) => {
    onSelectTab(tabId);
    setMobileMenuOpen(false);
  };

  return (
    <header className="bg-[#161b22] border-b border-[#30363d] sticky top-0 z-50">
      <div className="mx-auto px-4 sm:px-6 lg:px-8">
        <div className="flex items-center justify-between h-14 gap-2">

          {/* Logo & Mobile Menu Toggle Button */}
          <div className="flex items-center space-x-3">
            <button
              onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
              className="lg:hidden p-1.5 rounded-md text-[#8b949e] hover:text-[#c9d1d9] hover:bg-[#21262d] focus:outline-none transition-colors"
              aria-label="Toggle Navigation Menu"
            >
              {mobileMenuOpen ? <X className="w-5 h-5 text-[#58a6ff]" /> : <Menu className="w-5 h-5" />}
            </button>

            <div className="flex items-center space-x-3 cursor-pointer" onClick={() => onSelectTab('dashboard')}>
              <div className="bg-[#1f6feb] text-white px-2 py-0.5 rounded font-mono font-bold text-sm tracking-wider flex items-center">
                WORM
              </div>
            </div>
          </div>

          {/* Navigation Links (Desktop) */}
          <nav className="hidden lg:flex space-x-1">
            {tabs.map((tab) => {
              const active = currentTab === tab.id;
              return (
                <button
                  key={tab.id}
                  onClick={() => onSelectTab(tab.id)}
                  className={`flex items-center px-3 py-1.5 rounded-md text-xs font-mono font-medium transition-colors ${active
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
          <div className="flex items-center space-x-2 sm:space-x-3 shrink-0">
            {/* Live EPS */}
            <div className="flex items-center font-mono text-xs text-[#8b949e] bg-[#0d1117] px-2 py-1 rounded border border-[#30363d]">
              <span className="w-2 h-2 rounded-full bg-emerald-500 mr-1.5 animate-pulse" />
              <span className="font-semibold text-[#c9d1d9] mr-1">{eps.toFixed(1)}</span>
              <span className="hidden sm:inline">EPS</span>
            </div>

            {/* Invariant status */}
            <div className="hidden sm:block">
              {invariantValid === true && (
                <Badge variant="green">
                  <ShieldCheck className="w-3 h-3 mr-1" />
                  0-LOSS VALID
                </Badge>
              )}
              {invariantValid === false && (
                <Badge variant="red">
                  <AlertTriangle className="w-3 h-3 mr-1" />
                  INVARIANT BREACH
                </Badge>
              )}
              {invariantValid == null && (
                <Badge variant="amber">
                  <AlertTriangle className="w-3 h-3 mr-1" />
                  TELEMETRY OFFLINE
                </Badge>
              )}
            </div>

            {/* Air-Gapped Strict */}
            <div className="hidden sm:block">
              <Badge variant="purple">
                <Lock className="w-3 h-3 mr-1" />
                AIR-GAPPED
              </Badge>
            </div>
          </div>

        </div>

        {/* Mobile Navigation Menu Dropdown */}
        {mobileMenuOpen && (
          <nav aria-label="Mobile Navigation" className="lg:hidden border-t border-[#30363d] py-3 space-y-1.5 animate-dropdown bg-[#161b22] px-1">
            {tabs.map((tab) => {
              const active = currentTab === tab.id;
              return (
                <button
                  key={tab.id}
                  onClick={() => handleMobileTabClick(tab.id)}
                  className={`flex items-center w-full px-3 py-2 rounded-md text-xs font-mono font-medium transition-colors ${active
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
        )}

      </div>
    </header>
  );
};
