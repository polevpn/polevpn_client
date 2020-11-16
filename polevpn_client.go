package main

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
)

const (
	POLE_CLIENT_INIT        = 0
	POLE_CLIENT_RUNING      = 1
	POLE_CLIENT_CLOSED      = 2
	POLE_CLIENT_RECONNETING = 3
)

const (
	CH_TUN_DEVICE_WRITE_SIZE     = 2048
	HEART_BEAT_INTERVAL          = 10
	WEBSOCKET_RECONNECT_TIMES    = 12
	WEBSOCKET_RECONNECT_INTERVAL = 10
	WEBSOCKET_NO_HEARTBEAT_TIMES = 4
)

type PoleVpnClient struct {
	tunio             *TunIO
	wsconn            *WebSocketConn
	forwarder         *LocalForwarder
	state             int
	mutex             *sync.Mutex
	endpoint          string
	user              string
	pwd               string
	sni               string
	allocip           string
	lasttimeHeartbeat time.Time
	reconnecting      bool
	wg                *sync.WaitGroup
	mode              bool
}

func NewPoleVpnClient(mode bool) (*PoleVpnClient, error) {

	var err error

	tunio, err := NewTunIO(CH_TUN_DEVICE_WRITE_SIZE, mode)

	if err != nil {
		elog.Fatal("create tun fail", err)
		return nil, err
	}

	wsconn := NewWebSocketConn()

	forwarder, err := NewLocalForwarder()

	if err != nil {
		elog.Error("websocket connect fail", err)
		return nil, err
	}

	client := &PoleVpnClient{
		tunio:     tunio,
		wsconn:    wsconn,
		forwarder: forwarder,
		state:     POLE_CLIENT_INIT,
		mutex:     &sync.Mutex{},
		wg:        &sync.WaitGroup{},
		mode:      mode,
	}

	return client, nil
}

func (pc *PoleVpnClient) Start(endpoint string, user string, pwd string, sni string) error {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state != POLE_CLIENT_INIT {
		return errors.New("client stoped or not init")
	}

	pc.endpoint = endpoint
	pc.user = user
	pc.pwd = pwd
	pc.sni = sni

	var err error

	elog.Infof("connect to %v", endpoint)

	err = pc.wsconn.Connect(endpoint, user, pwd, "", sni)
	if err != nil {
		elog.Error("websocket connect fail", err)
		return err
	}

	elog.Info("connect ok")

	pc.wsconn.SetHandler(CMD_ALLOC_IPADDR, pc.handlerAllocAdressRespose)

	pc.wsconn.SetHandler(CMD_S2C_IPDATA, pc.handlerIPDataResponse)

	pc.wsconn.SetHandler(CMD_CLIENT_CLOSED, pc.handlerWSConnCloseEvent)
	pc.wsconn.SetHandler(CMD_HEART_BEAT, pc.handlerHeartBeatRespose)

	pc.forwarder.SetTunIO(pc.tunio)
	pc.tunio.SetWebSocketConn(pc.wsconn)
	pc.tunio.SetLocalForwarder(pc.forwarder)
	pc.wsconn.StartProcess()
	pc.forwarder.StartProcess()
	pc.tunio.StartProcess()
	pc.AskAllocIPAddress()
	pc.lasttimeHeartbeat = time.Now()
	go pc.HeartBeat()
	pc.state = POLE_CLIENT_RUNING
	pc.wg.Add(1)
	return nil
}

func (pc *PoleVpnClient) WaitStop() {
	pc.wg.Wait()
}

func (pc *PoleVpnClient) AskAllocIPAddress() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_ALLOC_IPADDR)
	pc.wsconn.Send(buf)
}

func (pc *PoleVpnClient) handlerAllocAdressRespose(pkt PolePacket, wsc *WebSocketConn) {

	av, err := anyvalue.NewFromJson(pkt.Payload())

	if err != nil {
		elog.Error("alloc address av decode fail", err)
		pc.Stop()
		return
	}

	ip1 := av.Get("ip").AsStr()
	dns := av.Get("dns").AsStr()

	if ip1 == "" {
		elog.Error("alloc ip fail,stop client")
		pc.Stop()
		return
	}

	ip := net.ParseIP(ip1).To4()
	ip2 := net.IPv4(ip[0], ip[1], ip[2], ip[3]-1).To4().String()

	elog.Infof("set tun device ip src ip:%v,dst ip:%v", ip1, ip2)
	err = pc.tunio.SetIPAddressAndEnable(ip1, ip2)
	if err != nil {
		elog.Error("set address fail", err)
		pc.Stop()
		return
	}

	err = pc.tunio.SetDnsServer(dns)

	if err != nil {
		elog.Error("set dns server fail", err)
		pc.Stop()
		return
	}

	pc.tunio.DelRoute("1/8")
	err = pc.tunio.AddRoute("1/8", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}
	pc.tunio.DelRoute("2/7")
	err = pc.tunio.AddRoute("2/7", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.tunio.DelRoute("4/6")
	err = pc.tunio.AddRoute("4/6", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.tunio.DelRoute("8/5")
	err = pc.tunio.AddRoute("8/5", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}
	pc.tunio.DelRoute("16/4")
	err = pc.tunio.AddRoute("16/4", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.tunio.DelRoute("32/3")
	err = pc.tunio.AddRoute("32/3", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.tunio.DelRoute("64/2")
	err = pc.tunio.AddRoute("64/2", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.tunio.DelRoute("128.0/1")
	err = pc.tunio.AddRoute("128.0/1", ip2)
	if err != nil {
		elog.Error("add route fail", err)
		pc.Stop()
		return
	}

	pc.allocip = ip1

}

func (pc *PoleVpnClient) handlerHeartBeatRespose(pkt PolePacket, wsc *WebSocketConn) {
	elog.Debug("received heartbeat")
	pc.lasttimeHeartbeat = time.Now()
}

func (pc *PoleVpnClient) handlerIPDataResponse(pkt PolePacket, wsc *WebSocketConn) {
	pc.tunio.Enqueue(pkt[POLE_PACKET_HEADER_LEN:])
}

func (pc *PoleVpnClient) handlerWSConnCloseEvent(pkt PolePacket, wsc *WebSocketConn) {
	elog.Info("ws client closed,start reconnect")
	pc.reconnect()
}

func (pc *PoleVpnClient) reconnect() {

	if pc.reconnecting == true {
		elog.Info("wsconn is reconnecting")
		return
	}

	pc.reconnecting = true
	pc.state = POLE_CLIENT_RECONNETING

	success := false
	for i := 0; i < WEBSOCKET_RECONNECT_TIMES; i++ {

		elog.Info("reconnecting")
		err := pc.wsconn.Connect(pc.endpoint, pc.user, pc.pwd, pc.allocip, pc.sni)
		if err != nil {
			if err == ErrNetwork {
				elog.Error("reconnect fail", err)
				elog.Error("reconnect 10 seconds later")
				time.Sleep(time.Second * WEBSOCKET_RECONNECT_INTERVAL)
				continue
			} else if err == ErrIPNotExist {
				elog.Error("ip address expired,reconnect and request new")
				pc.allocip = ""
			} else {
				elog.Error("server refuse connect")
				break
			}

		} else {
			elog.Info("reconnect ok")
			if pc.allocip == "" {
				pc.AskAllocIPAddress()
			}
			pc.wsconn.StartProcess()
			pc.SendHeartBeat()
			success = true
			pc.state = POLE_CLIENT_RUNING
			break
		}
	}
	if success == false {
		elog.Error("reconnet failed,stop client")
		pc.Stop()
	}
	pc.reconnecting = false
}

func (pc *PoleVpnClient) SendHeartBeat() {
	buf := make([]byte, POLE_PACKET_HEADER_LEN)
	PolePacket(buf).SetCmd(CMD_HEART_BEAT)
	pc.wsconn.Send(buf)
}

func (pc *PoleVpnClient) HeartBeat() {

	timer := time.NewTicker(time.Second * time.Duration(HEART_BEAT_INTERVAL))

	for range timer.C {
		if pc.state == POLE_CLIENT_CLOSED {
			timer.Stop()
			break
		}
		timeNow := time.Now()
		if timeNow.Sub(pc.lasttimeHeartbeat) > time.Second*HEART_BEAT_INTERVAL*WEBSOCKET_NO_HEARTBEAT_TIMES {
			elog.Error("have not recevied heartbeat for", WEBSOCKET_NO_HEARTBEAT_TIMES, "times,close connection and reconnet")
			pc.wsconn.Close(true)
			pc.lasttimeHeartbeat = timeNow
			continue
		}
		pc.SendHeartBeat()
	}

}

func (pc *PoleVpnClient) Stop() {

	pc.mutex.Lock()
	defer pc.mutex.Unlock()

	if pc.state == POLE_CLIENT_CLOSED {
		elog.Error("client have been closed")
		return
	}

	elog.Error("polevpn client closing")
	pc.forwarder.Close()
	pc.wsconn.Close(false)
	pc.tunio.Close()
	pc.state = POLE_CLIENT_CLOSED
	pc.wg.Done()
	elog.Error("polevpn client closed")

}
