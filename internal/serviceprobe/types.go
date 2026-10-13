// Package serviceprobe contains the fixed IP-only Service verification contract.
package serviceprobe

type Target struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	PodUID  string `json:"podUID,omitempty"`
}
