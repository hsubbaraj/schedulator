import { useState, useEffect, useCallback } from 'react';
import type { WorldState, EventRecord, SSEEvent, CycleSummary, ScalingConfig } from '../types';

const API_BASE = '/api/v1';

export function useWorldState() {
  const [state, setState] = useState<WorldState | null>(null);
  const [events, setEvents] = useState<EventRecord[]>([]);
  const [cycleHistory, setCycleHistory] = useState<EventRecord[]>([]);
  const [connected, setConnected] = useState(false);
  const [latestCycle, setLatestCycle] = useState<CycleSummary | null>(null);
  const [scalingConfig, setScalingConfig] = useState<ScalingConfig | null>(null);

  const fetchState = useCallback(async () => {
    try {
      const res = await fetch(`${API_BASE}/state`);
      if (res.ok) {
        const data = await res.json();
        setState(data);
      }
    } catch {
      // Will retry on next poll or SSE reconnect.
    }
  }, []);

  const fetchHistory = useCallback(async () => {
    try {
      const res = await fetch(`${API_BASE}/events/history?limit=50`);
      if (res.ok) {
        const data: EventRecord[] = await res.json();
        setEvents(data || []);
      }
    } catch {
      // Silent fail.
    }
  }, []);

  const fetchScalingConfig = useCallback(async () => {
    try {
      const res = await fetch(`${API_BASE}/scaling-config`);
      if (res.ok) {
        const data: ScalingConfig = await res.json();
        setScalingConfig(data);
      }
    } catch {
      // Silent fail.
    }
  }, []);

  const fetchCycleHistory = useCallback(async () => {
    try {
      const res = await fetch(`${API_BASE}/control-loop/history?limit=100`);
      if (res.ok) {
        const data: EventRecord[] = await res.json();
        setCycleHistory(data || []);
      }
    } catch {
      // Silent fail.
    }
  }, []);

  useEffect(() => {
    fetchState();
    fetchHistory();
    fetchScalingConfig();
    fetchCycleHistory();

    // SSE connection.
    const es = new EventSource(`${API_BASE}/events/stream`);

    es.onopen = () => setConnected(true);

    es.onmessage = (msg) => {
      try {
        const event: SSEEvent = JSON.parse(msg.data);
        if (event.type === 'state_update' && event.data) {
          setState(event.data as WorldState);
        }
        if (event.type === 'cycle_summary' && event.data) {
          setLatestCycle(event.data as CycleSummary);
          // Prepend to cycle history.
          setCycleHistory((prev) => [
            {
              id: Date.now(),
              timestamp: event.timestamp,
              type: event.type,
              app_id: '',
              cluster_id: '',
              summary: '',
              detail_json: JSON.stringify(event.data),
            },
            ...prev.slice(0, 99),
          ]);
        }
        // Add as a simple event record for the feed.
        const summary =
          event.type === 'cycle_summary' && event.data
            ? formatCycleSummary(event.data as CycleSummary)
            : `${event.type}`;
        setEvents((prev) => [
          {
            id: Date.now(),
            timestamp: event.timestamp,
            type: event.type,
            app_id: '',
            cluster_id: '',
            summary,
            detail_json: JSON.stringify(event.data),
          },
          ...prev.slice(0, 99),
        ]);
      } catch {
        // Ignore parse errors.
      }
    };

    es.onerror = () => {
      setConnected(false);
    };

    // Poll state every 10s as a fallback.
    const interval = setInterval(fetchState, 10000);

    return () => {
      es.close();
      clearInterval(interval);
    };
  }, [fetchState, fetchHistory, fetchScalingConfig, fetchCycleHistory]);

  return { state, events, cycleHistory, connected, refresh: fetchState, latestCycle, scalingConfig };
}

function formatCycleSummary(cs: CycleSummary): string {
  const parts = [`Cycle ${cs.cycle_duration_ms}ms`];
  if (cs.placement.scale_up_count > 0) parts.push(`+${cs.placement.scale_up_count} up`);
  if (cs.placement.scale_down_count > 0) parts.push(`-${cs.placement.scale_down_count} down`);
  if (cs.placement.preemption_count > 0) parts.push(`${cs.placement.preemption_count} preempt`);
  return parts.join(' | ');
}
