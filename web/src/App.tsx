import { useWorldState } from './hooks/useWorldState';
import HeroStrip from './components/HeroStrip';
import ClusterCard from './components/ClusterCard';
import AppTable from './components/AppTable';
import EventStream from './components/EventStream';
import GpuTimeline from './components/GpuTimeline';
import ReservationMonitor from './components/ReservationMonitor';
import ControlLoopTable from './components/ControlLoopTable';
import MetricInjectionPanel from './components/MetricInjectionPanel';
import type { Replica, GPUReservation } from './types';

export default function App() {
  const { state, events, cycleHistory, connected, latestCycle, scalingConfig } = useWorldState();

  const clusters = state ? Object.values(state.Clusters) : [];
  const applications = state ? Object.values(state.Applications) : [];
  const replicas: Replica[] = state ? Object.values(state.Replicas) : [];
  const reservations: GPUReservation[] = state?.Reservations ? Object.values(state.Reservations) : [];
  const cacheLocations = state?.CacheLocations || {};

  return (
    <div className="min-h-screen bg-gray-900 text-gray-100">
      {/* Header */}
      <header className="border-b border-gray-800 px-6 py-4">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-bold">Schedulator</h1>
            <p className="text-sm text-gray-400">GPU Scheduler Dashboard</p>
          </div>
          <div className="flex items-center gap-2">
            <span
              className={`w-2 h-2 rounded-full ${connected ? 'bg-green-500' : 'bg-red-500'}`}
            />
            <span className="text-sm text-gray-400">
              {connected ? 'Connected' : 'Disconnected'}
            </span>
            {state && (
              <span className="text-xs text-gray-500 ml-4">
                Last update: {new Date(state.TakenAt).toLocaleTimeString()}
              </span>
            )}
          </div>
        </div>
      </header>

      <div className="flex">
        {/* Main content */}
        <main className="flex-1 p-6 space-y-6">
          {/* Hero Status Strip */}
          {state && <HeroStrip state={state} latestCycle={latestCycle} />}

          {/* Control Loop Monitoring */}
          <section>
            <ControlLoopTable cycleHistory={cycleHistory} />
          </section>

          {/* Fleet Overview */}
          <section>
            <h2 className="text-xl font-semibold mb-4">Fleet Overview</h2>
            {clusters.length === 0 ? (
              <p className="text-gray-500">No clusters connected</p>
            ) : (
              <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                {clusters
                  .sort((a, b) => a.ClusterID.localeCompare(b.ClusterID))
                  .map((cluster) => (
                    <ClusterCard
                      key={cluster.ClusterID}
                      cluster={cluster}
                      reservations={reservations}
                      cacheLocations={cacheLocations}
                    />
                  ))}
              </div>
            )}
          </section>

          {/* Applications */}
          <section>
            <h2 className="text-xl font-semibold mb-4">Applications</h2>
            {applications.length === 0 ? (
              <p className="text-gray-500">No applications configured</p>
            ) : state ? (
              <AppTable
                applications={applications}
                replicas={replicas}
                state={state}
                latestCycle={latestCycle}
                scalingConfig={scalingConfig}
                cycleHistory={cycleHistory}
              />
            ) : null}
          </section>

          {/* GPU Timelines */}
          {clusters.length > 0 && (
            <section>
              <h2 className="text-xl font-semibold mb-4">GPU Utilization</h2>
              <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                {clusters.map((cluster) => (
                  <GpuTimeline
                    key={cluster.ClusterID}
                    clusterID={cluster.ClusterID}
                    reservations={reservations}
                  />
                ))}
              </div>
            </section>
          )}

          {/* Reservation Monitor */}
          <section>
            <ReservationMonitor reservations={reservations} />
          </section>

          {/* Simulation */}
          {state && (
            <section className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <MetricInjectionPanel state={state} />
            </section>
          )}
        </main>

        {/* Sidebar: Event Stream */}
        <aside className="w-96 border-l border-gray-800 p-4 hidden xl:block">
          <EventStream events={events} />
        </aside>
      </div>

      {/* Mobile event stream */}
      <div className="xl:hidden p-6">
        <EventStream events={events} />
      </div>
    </div>
  );
}
