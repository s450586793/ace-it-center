export interface Owner {
  id: string
  username: string
}

export interface Organization {
  id: string
  name: string
  created_at: string
}

export interface Site {
  id: string
  organization_id: string
  name: string
  created_at: string
}

export interface NodeGroup {
  id: string
  site_id: string
  name: string
  created_at: string
}

export interface Node {
  id: string
  group_id: string
  name: string
  type: 'windows' | 'linux'
  agent_version: string
  os_name: string
  os_version: string
  ip_address: string
  cpu_percent: number
  memory_percent: number
  disk_percent: number
  last_seen_at: string | null
  created_at: string
}

