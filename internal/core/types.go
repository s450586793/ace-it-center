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
	ID                         string     `json:"id"`
	GroupID                    string     `json:"group_id"`
	Remark                     string     `json:"remark"`
	Name                       string     `json:"name"`
	Type                       string     `json:"type"`
	AgentVersion               string     `json:"agent_version"`
	OSName                     string     `json:"os_name"`
	OSVersion                  string     `json:"os_version"`
	IPAddress                  string     `json:"ip_address"`
	CPUPercent                 float64    `json:"cpu_percent"`
	MemoryPercent              float64    `json:"memory_percent"`
	DiskPercent                float64    `json:"disk_percent"`
	NetworkMetricsAvailable    bool       `json:"network_metrics_available"`
	NetworkUploadMBPerSecond   float64    `json:"network_upload_mb_s"`
	NetworkDownloadMBPerSecond float64    `json:"network_download_mb_s"`
	NetworkUsageAvailable      bool       `json:"network_usage_available"`
	NetworkUsageDay            string     `json:"network_usage_day"`
	NetworkTodayUploadBytes    uint64     `json:"network_today_upload_bytes"`
	NetworkTodayDownloadBytes  uint64     `json:"network_today_download_bytes"`
	NetworkMonthUploadBytes    uint64     `json:"network_month_upload_bytes"`
	NetworkMonthDownloadBytes  uint64     `json:"network_month_download_bytes"`
	LastSeenAt                 *time.Time `json:"last_seen_at"`
	CreatedAt                  time.Time  `json:"created_at"`
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

type PairingState string

const (
	PairingPending  PairingState = "pending"
	PairingApproved PairingState = "approved"
	PairingRejected PairingState = "rejected"
	PairingExpired  PairingState = "expired"
)

type PairingRequest struct {
	ID             string       `json:"id"`
	MachineID      string       `json:"machine_id"`
	Hostname       string       `json:"hostname"`
	Type           string       `json:"type"`
	AgentVersion   string       `json:"agent_version"`
	CredentialHash string       `json:"-"`
	State          PairingState `json:"state"`
	ExistingNode   *Node        `json:"existing_node,omitempty"`
	GroupID        string       `json:"group_id,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	ExpiresAt      time.Time    `json:"expires_at"`
	DecidedAt      *time.Time   `json:"decided_at,omitempty"`
}

type PairingCreateRequest struct {
	Hostname          string `json:"hostname"`
	Type              string `json:"type"`
	AgentVersion      string `json:"agent_version"`
	MachineID         string `json:"machine_id"`
	PairingCredential string `json:"pairing_credential"`
}

type PairingPollResult struct {
	ID        string       `json:"pairing_id"`
	State     PairingState `json:"state"`
	Node      *Node        `json:"node,omitempty"`
	ExpiresAt time.Time    `json:"expires_at"`
}

type Heartbeat struct {
	Hostname                   string  `json:"hostname"`
	AgentVersion               string  `json:"agent_version"`
	OSName                     string  `json:"os_name"`
	OSVersion                  string  `json:"os_version"`
	IPAddress                  string  `json:"ip_address"`
	CPUPercent                 float64 `json:"cpu_percent"`
	MemoryPercent              float64 `json:"memory_percent"`
	DiskPercent                float64 `json:"disk_percent"`
	NetworkMetricsAvailable    bool    `json:"network_metrics_available"`
	NetworkUploadMBPerSecond   float64 `json:"network_upload_mb_s"`
	NetworkDownloadMBPerSecond float64 `json:"network_download_mb_s"`
	NetworkUsageAvailable      bool    `json:"network_usage_available"`
	NetworkUsageDay            string  `json:"network_usage_day"`
	NetworkTodayUploadBytes    uint64  `json:"network_today_upload_bytes"`
	NetworkTodayDownloadBytes  uint64  `json:"network_today_download_bytes"`
	NetworkMonthUploadBytes    uint64  `json:"network_month_upload_bytes"`
	NetworkMonthDownloadBytes  uint64  `json:"network_month_download_bytes"`
}

type NetworkHistoryPoint struct {
	CapturedAt                 time.Time `json:"captured_at"`
	UploadAverageMBPerSecond   float64   `json:"upload_avg_mb_s"`
	DownloadAverageMBPerSecond float64   `json:"download_avg_mb_s"`
	UploadPeakMBPerSecond      float64   `json:"upload_peak_mb_s"`
	DownloadPeakMBPerSecond    float64   `json:"download_peak_mb_s"`
}

type NetworkSummaryItem struct {
	NodeID                     string  `json:"node_id"`
	UploadAverageMBPerSecond   float64 `json:"upload_avg_mb_s"`
	DownloadAverageMBPerSecond float64 `json:"download_avg_mb_s"`
	UploadPeakMBPerSecond      float64 `json:"upload_peak_mb_s"`
	DownloadPeakMBPerSecond    float64 `json:"download_peak_mb_s"`
}

type AgentLogUpload struct {
	AgentLog  string `json:"agent_log"`
	UpdateLog string `json:"update_log"`
}

type AgentLogSnapshot struct {
	NodeID     string    `json:"node_id"`
	AgentLog   string    `json:"agent_log"`
	UpdateLog  string    `json:"update_log"`
	CapturedAt time.Time `json:"captured_at"`
}
