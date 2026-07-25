package core

import "time"

type Owner struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        string
	OwnerID   string
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
}

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Site struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
}

type NodeGroup struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Node struct {
	ID            string     `json:"id"`
	GroupID       string     `json:"group_id"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	AgentVersion  string     `json:"agent_version"`
	OSName        string     `json:"os_name"`
	OSVersion     string     `json:"os_version"`
	IPAddress     string     `json:"ip_address"`
	CPUPercent    float64    `json:"cpu_percent"`
	MemoryPercent float64    `json:"memory_percent"`
	DiskPercent   float64    `json:"disk_percent"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Enrollment struct {
	ID        string    `json:"id"`
	GroupID   string    `json:"group_id"`
	TokenHash string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
	Uses      int       `json:"uses"`
	CreatedAt time.Time `json:"created_at"`
}

type EnrollRequest struct {
	Token     string `json:"token"`
	Hostname  string `json:"hostname"`
	Type      string `json:"type"`
	Version   string `json:"version"`
	MachineID string `json:"machine_id"`
}

type Heartbeat struct {
	Hostname      string  `json:"hostname"`
	AgentVersion  string  `json:"agent_version"`
	OSName        string  `json:"os_name"`
	OSVersion     string  `json:"os_version"`
	IPAddress     string  `json:"ip_address"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	DiskPercent   float64 `json:"disk_percent"`
}
