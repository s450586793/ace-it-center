export interface Owner {
  id: string
  username: string
}

export interface NodeGroup {
  id: string
  site_id?: string
  name: string
  created_at: string
}

export interface Node {
  id: string
  group_id: string
  remark?: string
  name: string
  type: 'windows' | 'linux'
  agent_version: string
  os_name: string
  os_version: string
  ip_address: string
  cpu_percent: number
  memory_percent: number
  disk_percent: number
  network_metrics_available: boolean
  network_upload_mb_s: number
  network_download_mb_s: number
  network_usage_available?: boolean
  network_usage_day?: string
  network_today_upload_bytes?: number
  network_today_download_bytes?: number
  network_month_upload_bytes?: number
  network_month_download_bytes?: number
  last_seen_at: string | null
  created_at: string
}

export type PairingState = 'pending' | 'approved' | 'rejected' | 'expired'

export interface PairingRequest {
  id: string
  machine_id: string
  hostname: string
  type: string
  agent_version: string
  state: PairingState
  existing_node?: Node
  group_id?: string
  created_at: string
  expires_at: string
}

export type NetworkRange = '24h' | '7d' | '30d' | '90d'

export interface NetworkHistoryPoint {
  captured_at: string
  upload_avg_mb_s: number
  download_avg_mb_s: number
  upload_peak_mb_s: number
  download_peak_mb_s: number
}

export interface NetworkHistoryResponse {
  node_id: string
  range: NetworkRange
  unit: 'MB/s'
  points: NetworkHistoryPoint[]
}

export interface NetworkSummaryItem {
  node_id: string
  upload_avg_mb_s: number
  download_avg_mb_s: number
  upload_peak_mb_s: number
  download_peak_mb_s: number
}

export interface NetworkSummaryResponse {
  range: NetworkRange
  unit: 'MB/s'
  items: NetworkSummaryItem[]
}

export interface AgentLogSnapshot {
  node_id: string
  agent_log: string
  update_log: string
  captured_at: string
}

export type CommandShell = 'powershell' | 'cmd'
export type CommandStatus = 'queued' | 'leased' | 'running' | 'succeeded' | 'failed' | 'timed_out'

export interface CommandStatusCounts {
  queued: number
  leased: number
  running: number
  succeeded: number
  failed: number
  timed_out: number
}

export interface CommandTask {
  id: string
  shell: CommandShell
  command: string
  timeout_seconds: number
  created_by: string
  retried_from_id?: string
  created_at: string
  target_count: number
  counts: CommandStatusCounts
}

export interface CommandExecution {
  id: string
  task_id: string
  node_id: string
  node_name: string
  status: CommandStatus
  attempt: number
  started_at: string | null
  finished_at: string | null
  exit_code: number | null
  output: string
  output_truncated: boolean
  error_message: string
  duration_ms: number | null
}

export interface CommandTaskDetail {
  task: CommandTask
  executions: CommandExecution[]
}

export type SystemUpdateStage =
  | 'checking' | 'backing_up' | 'pulling' | 'switching_backend' | 'checking_backend'
  | 'switching_web' | 'checking_web' | 'stabilizing' | 'cleaning'
  | 'rolling_back' | 'succeeded' | 'failed' | 'manual_intervention'

export type CleanupStatus = 'not_run' | 'complete' | 'pending'

export interface SystemUpdateTask {
  id: string
  from: { backend: string; web: string }
  to: { backend: string; web: string }
  stage: SystemUpdateStage
  created_at: string
  started_at?: string
  finished_at?: string
  rolled_back: boolean
  cleanup: CleanupStatus
  error_code?: string
  error_message?: string
}

export interface SystemUpdateStatus {
  current: { backend: string; web: string }
  latest?: { backend: string; web: string; published_at?: string }
  update_available: boolean
  checked_at?: string
  task?: SystemUpdateTask
}
