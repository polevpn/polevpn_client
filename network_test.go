package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
)

func TestGetDefaultGateway(t *testing.T) {

	out, err := exec.Command("bash", "-c", "route -n get default |grep gateway").Output()
	if err != nil {
		t.Error(err)
	}

	gateway := strings.Replace(string(out), "gateway: ", "", -1)
	t.Log(gateway)
}

func TestLinuxGetDefaultGateway(t *testing.T) {

	out, err := exec.Command("bash", "-c", `ip route |grep default|grep -Eo "([0-9]{1,3}[\.]){3}[0-9]{1,3}"`).Output()
	if err != nil {
		t.Error(err)
	}

	gateway := string(out)
	t.Log(gateway)
}

func TestCidr(t *testing.T) {
	ip, network, err := net.ParseCIDR("10.9.3.254/31")
	if err != nil {
		t.Error(err)
	}
	fmt.Println(ip, network)
}
