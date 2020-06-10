// Package ipmi contains code for running and interpreting output of system ipmitool util
package ipmi

import (
	"regexp"
	"strings"

	"eos2git.cec.lab.emc.com/ECS/baremetal-csi-plugin.git/pkg/base/command"
)

const (
	// LanPrintCmd print bmc ip cmd with ipmitool
	LanPrintCmd = " ipmitool lan print"
)

// WrapIpmi is an interface that encapsulates operation with system ipmi util
type WrapIpmi interface {
	GetBmcIP() string
}

// IPMI is implementation for WrapImpi interface
type IPMI struct {
	e command.CmdExecutor
}

// NewIPMI is a constructor for LSBLK struct
func NewIPMI(e command.CmdExecutor) *IPMI {
	return &IPMI{e: e}
}

// GetBmcIP returns BMC IP using ipmitool
func (i *IPMI) GetBmcIP() string {
	/* Sample output
	IP Address Source       : DHCP Address
	IP Address              : 10.245.137.136
	*/

	strOut, _, err := i.e.RunCmd(LanPrintCmd)
	if err != nil {
		return ""
	}
	ipAddrStr := "ip address"
	var ip string
	//Regular expr to find ip address
	regex := regexp.MustCompile(`^(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$`)
	for _, str := range strings.Split(strOut, "\n") {
		str = strings.ToLower(str)
		if strings.Contains(str, ipAddrStr) {
			newStr := strings.Split(str, ":")
			if len(newStr) == 2 {
				s := strings.TrimSpace(newStr[1])
				matched := regex.MatchString(s)
				if matched {
					ip = s
				}
			}
		}
	}
	return ip
}