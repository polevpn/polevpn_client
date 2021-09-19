package main

import (
	"errors"
	"os/exec"
)

type LinuxNetworkManager struct {
	sysdns     string
	netservice string
}

func NewLinuxNetworkManager() *LinuxNetworkManager {
	return &LinuxNetworkManager{}
}

func (nm *LinuxNetworkManager) setIPAddressAndEnable(tundev string, ip1 string) error {

	var out []byte
	var err error

	out, err = exec.Command("bash", "-c", "ip addr add dev "+tundev+" local "+ip1+" peer "+ip1).Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}

	out, err = exec.Command("bash", "-c", "ip link set "+tundev+" up").Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return nil
}

func (nm *LinuxNetworkManager) setDnsServer(ip string, service string) error {

	return nil
}

func (nm *LinuxNetworkManager) removeDnsServer(service string) error {

	return nil
}

func (nm *LinuxNetworkManager) getNetServiceeDns() (string, string, error) {

	return "", "", errors.New("no net service have ip and dns")
}

func (nm *LinuxNetworkManager) addRoute(cidr string, gw string) error {
	out, err := exec.Command("bash", "-c", "ip route add "+cidr+" via "+gw).Output()
	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return err
}

func (nm *LinuxNetworkManager) delRoute(cidr string) error {

	out, err := exec.Command("bash", "-c", "ip route del "+cidr).Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return err

}

func (nm *LinuxNetworkManager) SetNetwork(device string, gateway string, remoteIp string, dns string, routes []string) error {

	var err error

	plog.Infof("set tun device ip as %v", gateway)
	err = nm.setIPAddressAndEnable(device, gateway)
	if err != nil {
		return errors.New("set address fail," + err.Error())
	}

	for _, route := range routes {
		plog.Info("add route ", route, "via", gateway)
		nm.delRoute(route)
		err = nm.addRoute(route, gateway)
		if err != nil {
			return errors.New("add route fail," + err.Error())
		}
	}
	return nil
}

func (nm *LinuxNetworkManager) RestoreNetwork() {

	plog.Infof("restore network service %v", nm.netservice)
	nm.removeDnsServer(nm.netservice)
	if nm.sysdns != "" {
		plog.Infof("set service %v dns to %v", nm.netservice, nm.sysdns)
		nm.setDnsServer(nm.sysdns, nm.netservice)
	}
}
