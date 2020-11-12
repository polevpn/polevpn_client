package main

import (
	"flag"
	"time"

	"github.com/polevpn/elog"
)

func init() {

}

func main() {
	var forwardcidr string
	flag.StringVar(&forwardcidr, "forwardip", "8.8.8.8", "forward ip")
	flag.Parse()
	defer elog.Flush()

	endpoint := "ws://27.125.149.37:8080/ws"
	user := "polevpn"
	pwd := "123456"

	client, err := NewPoleVpnClient()

	if err != nil {
		elog.Fatal("new polevpn client fail", err)
	}

	err = client.Start(endpoint, user, pwd, forwardcidr)

	if err != nil {
		elog.Fatal("start polevpn client fail", err)
	}
	time.Sleep(time.Hour)

}
