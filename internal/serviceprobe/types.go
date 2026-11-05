// Package serviceprobe contains the fixed IP-only Service verification contract.
package serviceprobe

import "time"

type Target struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	PodUID  string `json:"podUID,omitempty"`
}

type Contract struct {
	Method         string        `json:"method"`
	Path           string        `json:"path,omitempty"`
	ExpectedStatus int           `json:"expectedStatus,omitempty"`
	Timeout        time.Duration `json:"timeout"`
}
type Request struct {
	Nonce    string   `json:"nonce"`
	Targets  []Target `json:"targets"`
	Contract Contract `json:"contract"`
}
type Check struct {
	Target
	Success bool   `json:"success"`
	Status  int    `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}
type Result struct {
	Nonce  string  `json:"nonce"`
	Checks []Check `json:"checks"`
}
