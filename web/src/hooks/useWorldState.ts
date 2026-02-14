import { useState, useEffect, useCallback } from 'react';
import type { WorldState, EventRecord, SSEEvent } from '../types';

const API_BASE = '/api/v1';

export function useWorldState() {
  const [state, setState] = useState<WorldState | null>(null);
  const [events, setEvents] = useState<EventRecord[]>([]);
  const [connected, setConnected] = useState(false);

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

  useEffect(() => {
    fetchState();
    fetchHistory();

    // SSE connection.
    const es = new EventSource(`${API_BASE}/events/stream`);

    es.onopen = () => setConnected(true);

    es.onmessage = (msg) => {
      try {
        const event: SSEEvent = JSON.parse(msg.data);
        if (event.type === 'state_update' && event.data) {
          setState(event.data as WorldState);
        }
        // Add as a simple event record for the feed.
        setEvents((prev) => [
          {
            id: Date.now(),
            timestamp: event.timestamp,
            type: event.type,
            app_id: '',
            cluster_id: '',
            summary: `${event.type}`,
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
  }, [fetchState, fetchHistory]);

  return { state, events, connected, refresh: fetchState };
}
