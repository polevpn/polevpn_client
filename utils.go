package main

import (
	"errors"
	"net"
	"os"
	"runtime/debug"
	"strings"

	"github.com/polevpn/elog"
)

func PanicHandler() {
	if err := recover(); err != nil {
		elog.Error("Panic Exception:", err)
		elog.Error(string(debug.Stack()))
	}
}

func PanicHandlerExit() {
	if err := recover(); err != nil {
		elog.Error("Panic Exception:", err)
		elog.Error(string(debug.Stack()))
		elog.Error("************Program Exit************")
		os.Exit(0)
	}
}

func GetLocalIp() (string, error) {

	name, err := os.Hostname()

	if err != nil {
		return "", err
	}

	ipList, err := net.LookupHost(name)
	if err != nil {
		return "", err
	}

	for _, ip := range ipList {
		if !strings.HasPrefix(ip, "127.") && !strings.Contains(ip, "::") {
			return ip, nil
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	} else {
		for _, address := range addrs {
			if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String(), nil
				}
			}
		}
	}
	return "", errors.New("can't get local ip")
}
