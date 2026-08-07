package scvmm

// Host mirrors the fields we pull from Get-SCVMHost.
type Host struct {
	Name               string  `json:"Name"`
	Overall             string  `json:"OverallState"`
	CPUUsagePercent    float64 `json:"CPUUsagePercent"`
	TotalMemoryMB      float64 `json:"TotalMemoryMB"`
	AvailableMemoryMB  float64 `json:"AvailableMemoryMB"`
	ClusterName        string  `json:"ClusterName"`
}

// VM mirrors the fields we pull from Get-SCVirtualMachine.
type VM struct {
	ID                string  `json:"ID"`
	Name              string  `json:"Name"`
	Status            string  `json:"Status"` // PowerState: Running, PowerOff, Paused, etc.
	VMHostName        string  `json:"VMHostName"`
	CPUCount          int     `json:"CPUCount"`
	MemoryAssignedMB  float64 `json:"MemoryAssignedMB"`
	MemoryDemandMB    float64 `json:"MemoryDemandMB"`
	OperatingSystem   string  `json:"OperatingSystem"`
	Generation        int     `json:"Generation"`
}

// Volume mirrors Get-SCStorageVolume fields used for host capacity metrics.
type Volume struct {
	HostName  string  `json:"HostName"`
	Name      string  `json:"Name"`
	SizeBytes float64 `json:"SizeBytes"`
	FreeBytes float64 `json:"FreeBytes"`
}
