package main

import (
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/polevpn/elog"
)

var endpoint string
var user string
var pwd string
var sni string
var mode bool

func init() {
	flag.StringVar(&endpoint, "e", "", "server connect to")
	flag.StringVar(&user, "u", "", "login user")
	flag.StringVar(&pwd, "p", "", "login password")
	flag.StringVar(&sni, "s", "www.apple.com", "fake domain")
	flag.BoolVar(&mode, "m", false, "global route mode")
}

func signalHandler(pc *PoleVpnClient) {

	c := make(chan os.Signal)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range c {
			switch s {
			case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				if pc != nil {
					pc.Stop()
				}
				elog.Fatal("receive exit signal,exit")
			case syscall.SIGUSR1:
			case syscall.SIGUSR2:
			default:
			}
		}
	}()
}

func main() {

	flag.Parse()
	go func() {
		for range time.NewTicker(time.Second * 5).C {
			m := runtime.MemStats{}
			runtime.ReadMemStats(&m)
			elog.Printf("mem=%v,go=%v", m.HeapAlloc, runtime.NumGoroutine())
		}
	}()

	client, err := NewPoleVpnClient(mode)

	if err != nil {
		elog.Fatal("new polevpn client fail", err)
	}

	err = client.Start(endpoint, user, pwd, sni)
	if err != nil {
		elog.Fatal("start polevpn client fail", err)
	}

	signalHandler(client)

	client.WaitStop()
}
