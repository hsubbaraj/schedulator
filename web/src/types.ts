export interface Node {
  NodeID: string;
  ClusterID: string;
  TotalGPUs: number;
  AllocatedGPUs: number;
  FreeGPUs: number;
  Pods: string[] | null;
  CachedModels: Record<string, object> | null;
  Status: string;
}

export interface Cluster {
  ClusterID: string;
  Nodes: Record<string, Node> | null;
  FragmentationScore: number;
}

export interface Application {
  AppID: string;
  ModelID: string;
  GPUsPerReplica: number;
  Priority: number;
  MinReplicas: number;
  FailureDomainRule: string;
  SLA: SLA;
}

export interface SLA {
  AppID: string;
  MaxP99TTFTMs: number;
  MaxP99TPS: number;
}

export interface Replica {
  ReplicaID: string;
  AppID: string;
  ClusterID: string;
  NodeID: string;
  GPUs: number;
  Status: string;
  CreatedAt: string;
}

export interface VLLMMetrics {
  AppID: string;
  AggregatedFrom: number;
  AvgWaitingQueueDepth: number;
  MaxWaitingQueueDepth: number;
  AvgRunningQueueDepth: number;
  AvgBatchUtilization: number;
  AvgKVCacheUtilization: number;
  MaxKVCacheUtilization: number;
  TotalTokensPerSecond: number;
  AvgTimeToFirstTokenMs: number;
  P99TimeToFirstTokenMs: number;
  MeasuredAt: string;
}

export interface GPUReservation {
  ReservationID: string;
  ClusterID: string;
  NodeID: string;
  GPUs: number;
  ForAppID: string;
  CreatedAt: string;
  TTLSeconds: number;
  Status: string;
}

export interface ScalingHistory {
  AppID: string;
  LastScaleUpAt: string;
  LastScaleDownAt: string;
  ConsecutiveScaleDownSignals: number;
}

export interface CacheLocation {
  ModelID: string;
  ClusterID: string;
  NodeID: string;
  Tier: string;
  CachedAt: string;
}

export interface PerformanceProfile {
  AppID: string;
  TPSPerReplica: number;
  TTFTP99Ms: number;
  ColdStartSeconds: number;
  WarmStartSeconds: number;
}

export interface WorldState {
  Clusters: Record<string, Cluster>;
  Applications: Record<string, Application>;
  Replicas: Record<string, Replica>;
  VLLMMetrics: Record<string, VLLMMetrics>;
  Reservations: Record<string, GPUReservation>;
  ScalingHistory: Record<string, ScalingHistory>;
  CacheLocations: Record<string, CacheLocation[]>;
  PerformanceProfiles: Record<string, PerformanceProfile>;
  TakenAt: string;
}

export interface EventRecord {
  id: number;
  timestamp: string;
  type: string;
  app_id: string;
  cluster_id: string;
  summary: string;
  detail_json: string;
}

export interface ClusterSnapshot {
  id: number;
  timestamp: string;
  cluster_id: string;
  total_gpus: number;
  allocated_gpus: number;
  free_gpus: number;
  replica_count: number;
}

export interface SSEEvent {
  type: string;
  timestamp: string;
  data: unknown;
}

// --- cycle_summary SSE event payload ---

export interface CycleSummary {
  trigger_kinds: string[];
  scaling_decisions: Record<string, ScalingDecision>;
  placement: PlacementSummary;
  execution: ExecutionSummary;
  cycle_duration_ms: number;
}

export interface ScalingDecision {
  AppID: string;
  CurrentCount: number;
  TargetCount: number;
  Direction: string;
  Signal: string;
  SLABreach: boolean;
  ScaleActionApproved: boolean;
  ActionAt: string;
  ScaleDownSignalObserved: boolean;
}

export interface PlacementSummary {
  scale_up_count: number;
  scale_down_count: number;
  preemption_count: number;
  scale_ups: ScaleUpDecision[] | null;
  scale_downs: ScaleDownDecision[] | null;
  preemptions: PreemptionDecision[] | null;
}

export interface ScaleUpDecision {
  AppID: string;
  ClusterID: string;
  ReservationID: string;
}

export interface ScaleDownDecision {
  AppID: string;
  ReplicaID: string;
}

export interface PreemptionDecision {
  VictimReplicaID: string;
  VictimAppID: string;
  NodeID: string;
  ClusterID: string;
  GracePeriodSeconds: number;
  Reason: string;
}

export interface ExecutionSummary {
  completed_count: number;
  failed_count: number;
  aborted_count: number;
  completed: string[] | null;
  failed: FailedOp[] | null;
  aborted: string[] | null;
}

export interface FailedOp {
  OperationID: string;
  Type: string;
  Err: string;
}

// --- scaling config endpoint ---

export interface ScalingConfig {
  QueueHighWatermark: number;
  QueueTarget: number;
  QueueLowWatermark: number;
  KVCacheHighWatermark: number;
  KVCacheTarget: number;
  KVCacheLowWatermark: number;
  BatchLowWatermark: number;
  MaxScaleDownPerCycle: number;
  MaxScaleUpPerCycle: number;
  ScaleUpCooldownSeconds: number;
  ScaleDownCooldownSeconds: number;
  StabilizationCycles: number;
}
