package hyperv

// VMDiskEntry is one VM<->VHD path association, sourced from
// Get-VM | Get-VMHardDiskDrive on the local Hyper-V host.
type VMDiskEntry struct {
	VMId   string `json:"VMId"`
	VMName string `json:"VMName"`
	Path   string `json:"Path"`
}

// DiskMap resolves a raw Hyper-V Virtual Storage Device Perfmon instance
// string back to a VM, built from the latest VMDiskEntry snapshot. Same
// two-index shape as build-hyperv-vm-disk-map.ps1's byPath/byId JSON cache,
// kept in memory here instead of a file since builder and sampler now live
// in the same process.
type DiskMap struct {
	ByPath map[string]VMDiskEntry // normalized full VHD(X) path -> VM
	ByID   map[string]VMDiskEntry // VM GUID -> VM (fallback for .vmgs instances)
}

// StorageSample is one raw Get-Counter sample for the Hyper-V Virtual
// Storage Device object.
type StorageSample struct {
	Path         string  `json:"Path"`
	InstanceName string  `json:"InstanceName"`
	CookedValue  float64 `json:"CookedValue"`
}
