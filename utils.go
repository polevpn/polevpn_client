package main

import (
	"net"
	"net/url"
	"os"

	"github.com/polevpn/anyvalue"
)

func GetRemoteIPByEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)

	if err != nil {
		return "", err
	}

	addr, err := net.ResolveIPAddr("ip", u.Hostname())

	if err != nil {
		return "", err
	}

	return addr.String(), nil
}

func GetConfig(configfile string) (*anyvalue.AnyValue, error) {

	f, err := os.Open(configfile)
	if err != nil {
		return nil, err
	}
	return anyvalue.NewFromJsonReader(f)
}
