import { useState } from 'react';
import type { WorldState, CycleSummary, ScalingConfig } from '../types';

interface Props {
  state: WorldState;
  latestCycle: CycleSummary | null;
  scalingConfig: ScalingConfig | null;
}

type StageStatus = 'pass' | 'warn' | 'fail' | 'na';

interface Stage {
  label: string;
  status: StageStatus;
  detail: string;
}

export default function DiagnosticPanel({ state, latestCycle, scalingConfig }: Props) {
  const apps = Object.keys(state.Applications).sort();
  const [selectedApp, setSelectedApp] = useState<string>(apps[0] || '');

  const stages = selectedApp ? buildStages(selectedApp, state, latestCycle, scalingConfig) : [];

  const statusIcon: Record<StageStatus, string> = {
    pass: '\u2705',
    warn: '\u23F8\uFE0F',
    fail: '\u274C',
    na: '\u2796',
  };

  const statusColor: Record<StageStatus, string> = {
    pass: 'border-green-500/50 bg-green-900/20',
    warn: 'border-yellow-500/50 bg-yellow-900/20',
    fail: 'border-red-500/50 bg-red-900/20',
    na: 'border-gray-600 bg-gray-800',
  };

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <h3 className="text-lg font-semibold mb-3">Why Is Nothing Happening?</h3>
      <select
        className="bg-gray-700 border border-gray-600 rounded px-3 py-1.5 text-sm mb-4 w-full"
        value={selectedApp}
        onChange={(e) => setSelectedApp(e.target.value)}
      >
        {apps.length === 0 && <option value="">No apps</option>}
        {apps.map((id) => (
          <option key={id} value={id}>
            {id}
          </option>
        ))}
      </select>

      {selectedApp && stages.length > 0 && (
        <div className="flex flex-col gap-2">
          {stages.map((stage, i) => (
            <div key={i} className={`rounded-lg border p-3 ${statusColor[stage.status]}`}>
              <div className="flex items-center gap-2">
                <span className="text-lg">{statusIcon[stage.status]}</span>
                <span className="text-sm font-medium">{stage.label}</span>
              </div>
              <p className="text-xs text-gray-400 mt-1 ml-7">{stage.detail}</p>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function buildStages(appID: string, state: WorldState, cycle: CycleSummary | null, cfg: ScalingConfig | null): Stage[] {
  const stages: Stage[] = [];
  const now = Date.now();

  // Stage 1: Metrics Received?
  const m = state.VLLMMetrics?.[appID];
  if (!m) {
    stages.push({ label: '1. Metrics Received', status: 'fail', detail: 'No metrics for this app' });
  } else {
    const age = (now - new Date(m.MeasuredAt).getTime()) / 1000;
    if (age > 60) {
      stages.push({ label: '1. Metrics Received', status: 'fail', detail: `Stale: last received ${Math.round(age)}s ago` });
    } else {
      stages.push({ label: '1. Metrics Received', status: 'pass', detail: `Received ${Math.round(age)}s ago` });
    }
  }

  // Stage 2: Scaling Decision?
  const decision = cycle?.scaling_decisions?.[appID];
  if (!cycle) {
    stages.push({ label: '2. Scaling Decision', status: 'na', detail: 'Waiting for cycle data...' });
  } else if (!decision) {
    stages.push({ label: '2. Scaling Decision', status: 'na', detail: 'No decision for this app in latest cycle' });
  } else if (decision.Direction === 'unchanged') {
    stages.push({ label: '2. Scaling Decision', status: 'pass', detail: 'No scaling needed' });
  } else {
    stages.push({
      label: '2. Scaling Decision',
      status: 'pass',
      detail: `${decision.Direction} (${decision.Signal}): ${decision.CurrentCount} -> ${decision.TargetCount}`,
    });
  }

  // Stage 3: Blocked by Stability?
  const history = state.ScalingHistory?.[appID];
  if (!decision || decision.Direction === 'unchanged') {
    stages.push({ label: '3. Stability Check', status: 'na', detail: 'Not applicable' });
  } else if (decision.ScaleActionApproved) {
    stages.push({ label: '3. Stability Check', status: 'pass', detail: 'Action approved' });
  } else {
    // Check what blocked it
    const parts: string[] = [];
    if (history && cfg) {
      const upCd = new Date(history.LastScaleUpAt).getTime() + cfg.ScaleUpCooldownSeconds * 1000 - now;
      const downCd = new Date(history.LastScaleDownAt).getTime() + cfg.ScaleDownCooldownSeconds * 1000 - now;
      if (decision.Direction === 'up' && upCd > 0) {
        parts.push(`Scale-up cooldown: ${Math.ceil(upCd / 1000)}s remaining`);
      }
      if (decision.Direction === 'down' && downCd > 0) {
        parts.push(`Scale-down cooldown: ${Math.ceil(downCd / 1000)}s remaining`);
      }
      if (decision.ScaleDownSignalObserved && history.ConsecutiveScaleDownSignals < cfg.StabilizationCycles) {
        parts.push(`Stabilization: ${history.ConsecutiveScaleDownSignals}/${cfg.StabilizationCycles} signals`);
      }
    }
    stages.push({
      label: '3. Stability Check',
      status: 'warn',
      detail: parts.length > 0 ? parts.join('; ') : 'Action not approved (check cooldowns/stabilization)',
    });
  }

  // Stage 4: Placement Found?
  if (!decision || decision.Direction !== 'up') {
    stages.push({ label: '4. Placement Found', status: 'na', detail: 'Not applicable (no scale-up)' });
  } else {
    const scaleUps = cycle?.placement.scale_ups?.filter((su) => su.AppID === appID) || [];
    if (scaleUps.length > 0) {
      stages.push({
        label: '4. Placement Found',
        status: 'pass',
        detail: `Target: ${scaleUps.map((su) => su.ClusterID).join(', ')}`,
      });
    } else {
      stages.push({ label: '4. Placement Found', status: 'fail', detail: 'No placement found - check cluster capacity' });
    }
  }

  // Stage 5: Execution Succeeded?
  if (!cycle) {
    stages.push({ label: '5. Execution', status: 'na', detail: 'No cycle data' });
  } else {
    const exec = cycle.execution;
    if (exec.failed_count > 0) {
      const failedOps = exec.failed || [];
      const errStr = failedOps.map((f) => `${f.OperationID}: ${f.Err}`).join('; ');
      stages.push({
        label: '5. Execution',
        status: exec.completed_count > 0 ? 'warn' : 'fail',
        detail: `${exec.completed_count} completed, ${exec.failed_count} failed. ${errStr}`,
      });
    } else if (exec.completed_count > 0) {
      stages.push({ label: '5. Execution', status: 'pass', detail: `${exec.completed_count} operations completed` });
    } else {
      stages.push({ label: '5. Execution', status: 'na', detail: 'No operations executed' });
    }
  }

  return stages;
}
