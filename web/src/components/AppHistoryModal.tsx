import { useState, useMemo } from 'react';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer } from 'recharts';
import type { WorldState, Application, EventRecord, CycleSummary, ScalingConfig, Replica } from '../types';

interface Props {
  app: Application;
  state: WorldState;
  cycleHistory: EventRecord[];
  scalingConfig: ScalingConfig | null;
  onClose: () => void;
}

export default function AppHistoryModal({ app, state, cycleHistory, scalingConfig, onClose }: Props) {
  const [activeTab, setActiveTab] = useState<'history' | 'diagnostics'>('history');

  const historyData = useMemo(() => {
    return cycleHistory
      .map((ev) => {
        try {
          const summary = JSON.parse(ev.detail_json) as CycleSummary;
          const decision = summary.scaling_decisions[app.AppID];
          if (!decision) return null;
          return {
            time: new Date(ev.timestamp).toLocaleTimeString(),
            timestamp: new Date(ev.timestamp),
            current: decision.CurrentCount,
            running: decision.RunningCount || 0,
            pending: decision.PendingCount || 0,
            target: decision.TargetCount,
            signal: decision.Signal,
            direction: decision.Direction,
            approved: decision.ScaleActionApproved,
            slaBreach: decision.SLABreach,
          };
        } catch {
          return null;
        }
      })
      .filter((d): d is NonNullable<typeof d> => d !== null)
      .reverse();
  }, [cycleHistory, app.AppID]);

  const runningReplicas = Object.values(state.Replicas).filter(
    (r: Replica) => r.AppID === app.AppID && r.Status === 'Running'
  ).length;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
      <div className="bg-gray-900 border border-gray-700 rounded-xl shadow-2xl w-full max-w-5xl max-h-[90vh] flex flex-col">
        {/* Header */}
        <div className="flex items-center justify-between p-6 border-b border-gray-800">
          <div>
            <h2 className="text-2xl font-bold text-white">{app.AppID}</h2>
            <p className="text-gray-400 text-sm">Application Scaling History & Diagnostics</p>
          </div>
          <button
            onClick={onClose}
            className="p-2 hover:bg-gray-800 rounded-lg transition-colors text-gray-400 hover:text-white"
          >
            <span className="text-2xl">&times;</span>
          </button>
        </div>

        {/* Tabs */}
        <div className="flex border-b border-gray-800 px-6">
          <button
            className={`px-4 py-3 text-sm font-medium border-b-2 transition-colors ${
              activeTab === 'history'
                ? 'border-blue-500 text-blue-400'
                : 'border-transparent text-gray-500 hover:text-gray-300'
            }`}
            onClick={() => setActiveTab('history')}
          >
            Scaling History
          </button>
          <button
            className={`px-4 py-3 text-sm font-medium border-b-2 transition-colors ${
              activeTab === 'diagnostics'
                ? 'border-blue-500 text-blue-400'
                : 'border-transparent text-gray-500 hover:text-gray-300'
            }`}
            onClick={() => setActiveTab('diagnostics')}
          >
            Current Decision Trace
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-6">
          {activeTab === 'history' ? (
            <div className="space-y-6">
              {/* Summary Stats */}
              <div className="grid grid-cols-3 gap-4">
                <div className="bg-gray-800/50 rounded-lg p-4 border border-gray-700">
                  <div className="text-xs text-gray-500 uppercase tracking-wider mb-1">Running Replicas</div>
                  <div className="text-2xl font-bold text-green-400">{runningReplicas}</div>
                </div>
                <div className="bg-gray-800/50 rounded-lg p-4 border border-gray-700">
                  <div className="text-xs text-gray-500 uppercase tracking-wider mb-1">Min Replicas</div>
                  <div className="text-2xl font-bold text-blue-400">{app.MinReplicas}</div>
                </div>
                <div className="bg-gray-800/50 rounded-lg p-4 border border-gray-700">
                  <div className="text-xs text-gray-500 uppercase tracking-wider mb-1">Current Target</div>
                  <div className="text-2xl font-bold text-purple-400">{historyData[historyData.length - 1]?.target ?? '-'}</div>
                </div>
              </div>

              {/* Scaling Chart */}
              <div className="bg-gray-950 rounded-lg p-6 border border-gray-800">
                <h4 className="text-sm font-semibold mb-6 text-gray-300">Target vs. Running Replicas (Last 100 Cycles)</h4>
                <div className="h-80 w-full">
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={historyData.slice(-100)}>
                      <CartesianGrid strokeDasharray="3 3" stroke="#374151" vertical={false} />
                      <XAxis 
                        dataKey="time" 
                        stroke="#9CA3AF" 
                        fontSize={10} 
                        tickLine={false}
                        axisLine={false}
                      />
                      <YAxis 
                        stroke="#9CA3AF" 
                        fontSize={10} 
                        tickLine={false}
                        axisLine={false}
                        allowDecimals={false}
                      />
                      <Tooltip 
                        contentStyle={{ backgroundColor: '#111827', borderColor: '#374151', fontSize: '12px' }}
                        itemStyle={{ fontSize: '12px' }}
                      />
                      <Legend iconType="circle" wrapperStyle={{ fontSize: '12px', paddingTop: '20px' }} />
                      <Line 
                        type="stepAfter" 
                        dataKey="target" 
                        name="Target Count" 
                        stroke="#8B5CF6" 
                        strokeWidth={2} 
                        dot={false}
                        activeDot={{ r: 4 }}
                      />
                      <Line 
                        type="stepAfter" 
                        dataKey="running" 
                        name="Running" 
                        stroke="#10B981" 
                        strokeWidth={2} 
                        dot={false}
                      />
                      <Line 
                        type="stepAfter" 
                        dataKey="pending" 
                        name="Pending" 
                        stroke="#F59E0B" 
                        strokeWidth={1} 
                        strokeDasharray="4 4"
                        dot={false}
                      />
                    </LineChart>
                  </ResponsiveContainer>
                </div>
              </div>

              {/* Scaling Logs */}
              <div>
                <h4 className="text-sm font-semibold mb-3 text-gray-300">Recent Scaling Events</h4>
                <div className="space-y-2">
                  {historyData.filter(d => d.approved && d.direction !== 'unchanged').slice(-10).reverse().map((d, i) => (
                    <div key={i} className="flex items-center justify-between p-3 bg-gray-800 rounded-lg border border-gray-700">
                      <div className="flex items-center gap-3">
                        <span className={`px-2 py-0.5 rounded text-xs font-bold ${
                          d.direction === 'up' ? 'bg-blue-900 text-blue-300' : 'bg-orange-900 text-orange-300'
                        }`}>
                          SCALE {d.direction.toUpperCase()}
                        </span>
                        <span className="text-sm text-gray-200">
                          {d.current} to {d.target} replicas
                        </span>
                      </div>
                      <div className="text-xs text-gray-500">
                        {d.timestamp.toLocaleTimeString()} via {d.signal}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          ) : (
            <div className="space-y-6">
               <DecisionTrace appID={app.AppID} state={state} latestCycle={historyData[historyData.length - 1]} cfg={scalingConfig} />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// Logic migrated from DiagnosticPanel for deep visibility
function DecisionTrace({ appID, state, latestCycle, cfg }: { appID: string, state: WorldState, latestCycle: any, cfg: ScalingConfig | null }) {
  if (!latestCycle) return <div className="text-gray-500 text-center py-10">No cycle data available for this application.</div>;

  const history = state.ScalingHistory?.[appID];
  const now = Date.now();

  const cooldowns = [];
  if (history && cfg) {
    const upCd = new Date(history.LastScaleUpAt).getTime() + cfg.ScaleUpCooldownSeconds * 1000 - now;
    const downCd = new Date(history.LastScaleDownAt).getTime() + cfg.ScaleDownCooldownSeconds * 1000 - now;
    if (upCd > 0) cooldowns.push({ label: 'Scale-up Cooldown', remaining: `${Math.ceil(upCd / 1000)}s` });
    if (downCd > 0) cooldowns.push({ label: 'Scale-down Cooldown', remaining: `${Math.ceil(downCd / 1000)}s` });
  }

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <div className="space-y-4">
        <h4 className="text-sm font-semibold text-gray-400 uppercase tracking-wider">Controller State</h4>
        <div className="bg-gray-800 rounded-xl p-5 border border-gray-700 space-y-4">
          <div className="flex justify-between items-center pb-3 border-b border-gray-700">
            <span className="text-gray-400">Direction</span>
            <span className={`font-bold ${latestCycle.direction === 'up' ? 'text-blue-400' : latestCycle.direction === 'down' ? 'text-orange-400' : 'text-gray-400'}`}>
              {latestCycle.direction.toUpperCase()}
            </span>
          </div>
          <div className="flex justify-between items-center pb-3 border-b border-gray-700">
            <span className="text-gray-400">Signal Source</span>
            <span className="text-white font-medium">{latestCycle.signal}</span>
          </div>
          <div className="flex justify-between items-center pb-3 border-b border-gray-700">
            <span className="text-gray-400">Approved?</span>
            <span className={latestCycle.approved ? 'text-green-400' : 'text-red-400'}>
              {latestCycle.approved ? 'YES' : 'NO'}
            </span>
          </div>
          <div className="flex justify-between items-center">
            <span className="text-gray-400">SLA Breach?</span>
            <span className={latestCycle.slaBreach ? 'text-red-400' : 'text-green-400'}>
              {latestCycle.slaBreach ? 'YES' : 'NO'}
            </span>
          </div>
        </div>
      </div>

      <div className="space-y-4">
        <h4 className="text-sm font-semibold text-gray-400 uppercase tracking-wider">Stability Controls</h4>
        <div className="bg-gray-800 rounded-xl p-5 border border-gray-700 space-y-4">
          {cooldowns.length > 0 ? cooldowns.map((c, i) => (
            <div key={i} className="flex justify-between items-center p-3 bg-red-900/20 border border-red-500/30 rounded-lg">
              <span className="text-red-200 text-sm">{c.label}</span>
              <span className="text-red-400 font-bold">{c.remaining}</span>
            </div>
          )) : (
            <div className="text-green-400 text-sm p-3 bg-green-900/20 border border-green-500/30 rounded-lg">
              No active cooldowns
            </div>
          )}
          
          <div className="p-3 bg-gray-900/50 rounded-lg border border-gray-700">
            <div className="flex justify-between items-center mb-2">
              <span className="text-gray-400 text-xs">Stabilization Signals</span>
              <span className="text-white text-xs font-bold">
                {history?.ConsecutiveScaleDownSignals ?? 0} / {cfg?.StabilizationCycles ?? 0}
              </span>
            </div>
            <div className="w-full bg-gray-700 rounded-full h-1.5">
              <div 
                className="bg-orange-500 h-1.5 rounded-full transition-all duration-500" 
                style={{ width: `${((history?.ConsecutiveScaleDownSignals ?? 0) / (cfg?.StabilizationCycles ?? 1)) * 100}%` }}
              />
            </div>
            <p className="text-[10px] text-gray-500 mt-2 italic">
              * Requires {cfg?.StabilizationCycles} consecutive signals to scale down
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}
