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
    normal: 'border-[#30363d]',
    good: 'border-emerald-800/80',
    warning: 'border-amber-800/80',
    error: 'border-rose-800/80',
  };

  const textColors = {
    normal: 'text-[#f0f6fc]',
    good: 'text-emerald-400',
    warning: 'text-amber-400',
    error: 'text-rose-400',
  };

  return (
    <div className={`bg-[#161b22] border ${borderColors[status]} rounded-md p-4 flex flex-col justify-between`}>
      <div className="flex items-center justify-between text-[#8b949e] text-xs uppercase tracking-wider font-mono mb-2">
        <span>{label}</span>
        {icon && <span className="opacity-70">{icon}</span>}
      </div>
      <div className="flex items-baseline gap-2">
        <span className={`text-2xl font-mono font-bold ${textColors[status]}`}>
          {typeof value === 'number' ? value.toLocaleString() : value}
        </span>
      </div>
      {subValue && (
        <div className="mt-1 text-xs font-mono text-[#8b949e]">
          {subValue}
        </div>
      )}
    </div>
  );
};
