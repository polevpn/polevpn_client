package main

import (
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
