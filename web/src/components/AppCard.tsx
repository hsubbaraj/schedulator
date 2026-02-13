import type { Application, Replica } from '../types';

interface Props {
  app: Application;
  replicas: Replica[];
}

export default function AppCard({ app, replicas }: Props) {
  const running = replicas.filter((r) => r.Status === 'running').length;
  const pending = replicas.filter((r) => r.Status === 'pending').length;

  const priorityLabel = ['P0 (Critical)', 'P1 (High)', 'P2 (Normal)', 'P3 (Low)'][app.Priority] || `P${app.Priority}`;

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-lg font-semibold">{app.AppID}</h3>
        <span className="text-xs px-2 py-0.5 rounded-full bg-gray-700 text-gray-300">
          {priorityLabel}
        </span>
      </div>
      <div className="text-sm text-gray-400 space-y-1">
        <p>Model: {app.ModelID}</p>
        <p>GPUs/replica: {app.GPUsPerReplica}</p>
        <p>Min replicas: {app.MinReplicas}</p>
        <p>Failure domain: {app.FailureDomainRule || 'none'}</p>
      </div>
      <div className="mt-3 flex gap-3">
        <div className="flex items-center gap-1.5">
          <span className="w-2 h-2 rounded-full bg-green-500" />
          <span className="text-sm">{running} running</span>
        </div>
        {pending > 0 && (
          <div className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-full bg-yellow-500" />
            <span className="text-sm">{pending} pending</span>
          </div>
        )}
      </div>
    </div>
  );
}
