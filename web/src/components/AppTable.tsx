import { useState } from 'react';
import type { Application, Replica, WorldState, CycleSummary, ScalingConfig, EventRecord } from '../types';
import AppHistoryModal from './AppHistoryModal';

interface Props {
  applications: Application[];
  replicas: Replica[];
  state: WorldState;
  latestCycle: CycleSummary | null;
  scalingConfig: ScalingConfig | null;
  cycleHistory: EventRecord[];
}

export default function AppTable({ applications, replicas, state, latestCycle, scalingConfig, cycleHistory }: Props) {
  const [selectedAppID, setSelectedAppID] = useState<string | null>(null);

  const metrics = state.VLLMMetrics || {};
  const decisions = latestCycle?.scaling_decisions || {};

  const sorted = [...applications].sort((a, b) => a.Priority - b.Priority || a.AppID.localeCompare(b.AppID));

  const selectedApp = applications.find(a => a.AppID === selectedAppID);

  return (
    <>
      <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-700 text-gray-400 text-xs uppercase tracking-wider">
              <th className="text-left p-3">App ID</th>
              <th className="text-center p-3">Priority</th>
              <th className="text-center p-3">Min</th>
              <th className="text-center p-3">Running</th>
              <th className="text-center p-3">Pending</th>
              <th className="text-center p-3">Target</th>
              <th className="text-center p-3">TTFT P99</th>
              <th className="text-center p-3">SLA</th>
              <th className="text-center p-3">Queue</th>
              <th className="text-center p-3">Last Action</th>
            </tr>
          </thead>
          <tbody>
            {sorted.map((app) => {
              const appReplicas = replicas.filter((r) => r.AppID === app.AppID);
              const running = appReplicas.filter((r) => r.Status === 'running').length;
              const pending = appReplicas.filter((r) => r.Status === 'pending').length;
              const m = metrics[app.AppID];
              const d = decisions[app.AppID];
              const ttft = m?.P99TimeToFirstTokenMs;
              const slaTarget = app.SLA?.MaxP99TTFTMs || 0;
              const slaBreach = slaTarget > 0 && ttft !== undefined && ttft > slaTarget;

              return (
                <tr
                  key={app.AppID}
                  className="border-b border-gray-700/50 hover:bg-gray-750 cursor-pointer transition-colors"
                  onClick={() => setSelectedAppID(app.AppID)}
                >
                  <td className="p-3 font-medium">{app.AppID}</td>
                  <td className="p-3 text-center">
                    <span className="px-2 py-0.5 rounded-full bg-gray-700 text-xs">P{app.Priority}</span>
                  </td>
                  <td className="p-3 text-center text-gray-400">{app.MinReplicas}</td>
                  <td className="p-3 text-center text-green-400">{running}</td>
                  <td className="p-3 text-center text-yellow-400">{pending || '-'}</td>
                  <td className="p-3 text-center">{d ? d.TargetCount : '-'}</td>
                  <td className="p-3 text-center">{ttft !== undefined ? `${ttft.toFixed(0)}ms` : '-'}</td>
                  <td className="p-3 text-center">
                    {slaTarget > 0 ? (
                      <span className={slaBreach ? 'text-red-400 font-medium' : 'text-green-400'}>
                        {slaBreach ? 'BREACH' : 'OK'}
                      </span>
                    ) : (
                      <span className="text-gray-500">-</span>
                    )}
                  </td>
                  <td className="p-3 text-center">{m ? m.AvgWaitingQueueDepth.toFixed(1) : '-'}</td>
                  <td className="p-3 text-center">
                    {d && d.Direction !== 'unchanged' ? (
                      <span
                        className={
                          d.Direction === 'up' ? 'text-green-400' : d.Direction === 'down' ? 'text-orange-400' : ''
                        }
                      >
                        {d.Direction} ({d.Signal})
                      </span>
                    ) : (
                      <span className="text-gray-500">-</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {selectedApp && (
        <AppHistoryModal
          app={selectedApp}
          state={state}
          cycleHistory={cycleHistory}
          scalingConfig={scalingConfig}
          onClose={() => setSelectedAppID(null)}
        />
      )}
    </>
  );
}
