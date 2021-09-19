package main

import (
	"net"
	"net/url"
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
