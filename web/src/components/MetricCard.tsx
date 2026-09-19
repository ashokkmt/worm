import React from 'react';

interface MetricCardProps {
  label: string;
  value: string | number;
  subValue?: string;
  status?: 'normal' | 'good' | 'warning' | 'error';
  icon?: React.ReactNode;
}

export const MetricCard: React.FC<MetricCardProps> = ({
  label,
  value,
  subValue,
  status = 'normal',
  icon,
}) => {
  const borderColors = {
    normal: 'border-[#30363d] hover:border-[#484f58]',
    good: 'border-emerald-800/80 hover:border-emerald-600/80',
    warning: 'border-amber-800/80 hover:border-amber-600/80',
    error: 'border-rose-800/80 hover:border-rose-600/80',
  };

  const textColors = {
    normal: 'text-[#f0f6fc]',
    good: 'text-emerald-400',
    warning: 'text-amber-400',
    error: 'text-rose-400',
  };

  return (
    <div className={`bg-[#161b22] border ${borderColors[status]} rounded-lg p-4 flex flex-col justify-between transition-all duration-150 shadow-sm relative overflow-hidden group min-w-0`}>
      <div className="flex items-center justify-between text-[#8b949e] text-[11px] uppercase tracking-wider font-mono font-semibold mb-2">
        <span className="truncate">{label}</span>
        {icon && <span className="opacity-80 group-hover:scale-110 transition-transform duration-150 shrink-0 ml-1">{icon}</span>}
      </div>
      <div className="flex items-baseline gap-2 min-w-0">
        <span className={`text-2xl font-mono font-bold tracking-tight ${textColors[status]} truncate`}>
          {typeof value === 'number' ? value.toLocaleString() : value}
        </span>
      </div>
      {subValue && (
        <div className="mt-1.5 text-xs font-mono text-[#8b949e] truncate flex items-center">
          {subValue}
        </div>
      )}
    </div>
  );
};
