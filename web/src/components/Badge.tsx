import React from 'react';

interface BadgeProps {
  children: React.ReactNode;
  variant?: 'green' | 'blue' | 'amber' | 'red' | 'purple' | 'gray';
  size?: 'sm' | 'md';
}

export const Badge: React.FC<BadgeProps> = ({
  children,
  variant = 'gray',
  size = 'sm',
}) => {
  const variantStyles = {
    green: 'bg-emerald-950/80 text-emerald-400 border border-emerald-800/80 shadow-xs',
    blue: 'bg-blue-950/80 text-blue-400 border border-blue-800/80 shadow-xs',
    amber: 'bg-amber-950/80 text-amber-400 border border-amber-800/80 shadow-xs',
    red: 'bg-rose-950/80 text-rose-400 border border-rose-800/80 shadow-xs',
    purple: 'bg-purple-950/80 text-purple-300 border border-purple-800/80 shadow-xs',
    gray: 'bg-[#21262d] text-[#8b949e] border border-[#30363d]',
  };

  const sizeStyles = {
    sm: 'px-2 py-0.5 text-[11px] font-mono leading-tight font-medium',
    md: 'px-2.5 py-1 text-xs font-mono font-semibold leading-tight',
  };

  return (
    <span className={`inline-flex items-center shrink-0 rounded-[10%] tracking-wide transition-colors ${sizeStyles[size]} ${variantStyles[variant]}`}>
      {children}
    </span>
  );
};
