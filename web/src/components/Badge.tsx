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
    green: 'bg-emerald-950/70 text-emerald-400 border border-emerald-800/80',
    blue: 'bg-blue-950/70 text-blue-400 border border-blue-800/80',
    amber: 'bg-amber-950/70 text-amber-400 border border-amber-800/80',
    red: 'bg-rose-950/70 text-rose-400 border border-rose-800/80',
    purple: 'bg-purple-950/70 text-purple-400 border border-purple-800/80',
    gray: 'bg-[#21262d] text-[#8b949e] border border-[#30363d]',
  };

  const sizeStyles = {
    sm: 'px-1.5 py-0.5 text-xs font-mono',
    md: 'px-2.5 py-1 text-xs font-mono font-medium',
  };

  return (
    <span className={`inline-flex items-center rounded ${sizeStyles[size]} ${variantStyles[variant]}`}>
      {children}
    </span>
  );
};
