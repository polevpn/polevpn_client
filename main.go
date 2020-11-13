package main

import (
	"flag"
	"runtime"
	"time"

	"github.com/polevpn/elog"
)

func init() {

}

func main() {

	flag.Parse()
	defer elog.Flush()

	endpoint := "ws://27.125.149.37:8080/ws"
	user := "polevpn"
	pwd := "123456"

	client, err := NewPoleVpnClient()

	if err != nil {
		elog.Fatal("new polevpn client fail", err)
	}

	err = client.Start(endpoint, user, pwd)

	if err != nil {
		elog.Fatal("start polevpn client fail", err)
	}

	for range time.NewTicker(time.Second * 5).C {
		m := runtime.MemStats{}
		runtime.ReadMemStats(&m)
		elog.Printf("mem=%v,go=%v", m.HeapAlloc, runtime.NumGoroutine())
	}

	time.Sleep(time.Hour)

}
