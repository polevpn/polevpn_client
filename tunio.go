package main

import (
	"errors"
	"io"
	"net"
	"os/exec"

	"github.com/polevpn/elog"
	"github.com/polevpn/geoip"
	"github.com/polevpn/netstack/tcpip/header"
	"github.com/polevpn/netstack/tcpip/transport/icmp"
	"github.com/polevpn/netstack/tcpip/transport/tcp"
	"github.com/polevpn/netstack/tcpip/transport/udp"
	"github.com/songgao/water"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	IP4_HEADER_LEN = 20
	TCP_HEADER_LEN = 20
	UDP_HEADER_LEN = 8
	DNS_PORT       = 53
)

type TunIO struct {
	ifce      *water.Interface
	wch       chan []byte
	mtu       int
	wsconn    *WebSocketConn
	forwarder *LocalForwarder
	closed    bool
	mode      bool
}

func NewTunIO(size int, mode bool) (*TunIO, error) {

	config := water.Config{
		DeviceType: water.TUN,
	}
	ifce, err := water.New(config)
	if err != nil {
		return nil, err
	}

	return &TunIO{
		ifce:   ifce,
		wch:    make(chan []byte, size),
		mtu:    1500,
		closed: false,
		mode:   mode}, nil
}

func (t *TunIO) SetWebSocketConn(wsconn *WebSocketConn) {
	t.wsconn = wsconn
}

func (t *TunIO) SetLocalForwarder(forwarder *LocalForwarder) {
	t.forwarder = forwarder
}

func (t *TunIO) SetIPAddressAndEnable(ip1 string, ip2 string) error {

	out, err := exec.Command("bash", "-c", "ifconfig "+t.ifce.Name()+" "+ip1+" "+ip2+" up").Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return nil
}

func (t *TunIO) AddRoute(cidr string, gw string) error {

	out, err := exec.Command("bash", "-c", "route -n add -net "+cidr+" "+gw).Output()

	if err != nil {
		return errors.New(err.Error() + "," + string(out))
	}
	return err

}

func (t *TunIO) Close() error {

	if t.closed == true {
		return nil
	}
	if t.wch != nil {
		t.wch <- nil
		close(t.wch)
	}
	t.closed = true
	return t.ifce.Close()
}

func (t *TunIO) IsClosed() bool {
	return t.closed
}

func (t *TunIO) StartProcess() {
	go t.read()
	go t.write()
}

func (t *TunIO) read() {

	defer func() {
		t.Close()
	}()

	defer PanicHandler()

	for {
		pkt := make([]byte, t.mtu)
		n, err := t.ifce.Read(pkt)
		if err != nil {
			elog.Error("read pkg from tun fail", err)
			return
		}
		pkt = pkt[:n]
		t.dispatch(pkt)
	}

}

func (t *TunIO) dispatch(pkt []byte) {

	var err error
	ipv4pkg := header.IPv4(pkt)
	direct := false
	if ipv4pkg.Protocol() == uint8(icmp.ProtocolNumber4) {
		if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
			//elog.Printf("ICMP CN IP %v", ipv4pkg.DestinationAddress().To4().String())
			direct = true
		}
	} else if ipv4pkg.Protocol() == uint8(tcp.ProtocolNumber) {
		tcppkg := header.TCP(pkt[IP4_HEADER_LEN:])
		if tcppkg.DestinationPort() == DNS_PORT {
			elog.Info("tcp dns request")
		}

		if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
			//elog.Printf("TCP CN IP %v", ipv4pkg.DestinationAddress().To4().String())
			direct = true
		}
	} else if ipv4pkg.Protocol() == uint8(udp.ProtocolNumber) {
		udppkg := header.UDP(pkt[IP4_HEADER_LEN:])
		if udppkg.DestinationPort() == DNS_PORT {
			var msg dnsmessage.Message
			err = msg.Unpack(pkt[IP4_HEADER_LEN+UDP_HEADER_LEN:])
			if err != nil {
				elog.Error("parser dns packet fail", err)
			} else {
				for i := 0; i < len(msg.Questions); i++ {
					name := msg.Questions[i].Name.String()
					name = name[:len(name)-1]
					if geoip.IsDirectDomainRecursive(name) {
						elog.Printf("DNS CN DOMAIN %v", name)
						direct = true
					}
				}
			}
		} else {
			if geoip.QueryCountryByIP(net.IP(ipv4pkg.DestinationAddress().To4())) == "CN" {
				//elog.Printf("UDP CN IP %v", ipv4pkg.DestinationAddress().To4().String())
				direct = true
			}
		}

	} else {
		elog.Info("unknown packet")
		return
	}

	if mode {
		direct = false
	}

	if direct {
		//elog.Info("to local forwarder")
		t.sendIPPacketToLocalForwarder(pkt)
	} else {
		//elog.Info("to remote forwarder")
		t.sendIPPacketToRemoteWSConn(pkt)
	}

}

func (t *TunIO) sendIPPacketToLocalForwarder(pkt []byte) {
	if t.forwarder != nil {
		t.forwarder.Write(pkt)
	} else {
		elog.Error("local forwarder haven't set")
	}
}

func (t *TunIO) sendIPPacketToRemoteWSConn(pkt []byte) {

	if t.wsconn != nil {
		buf := make([]byte, POLE_PACKET_HEADER_LEN+len(pkt))
		copy(buf[POLE_PACKET_HEADER_LEN:], pkt)
		polepkt := PolePacket(buf)
		polepkt.SetCmd(CMD_C2S_IPDATA)
		t.wsconn.Send(polepkt)
	} else {
		elog.Error("remote ws conn haven't set")
	}

}

func (t *TunIO) write() {
	defer PanicHandler()
	for {
		select {
		case pkt, ok := <-t.wch:
			if !ok {
				elog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					elog.Info("exit write process")
					return
				}
				_, err := t.ifce.Write(pkt)
				if err != nil {
					if err == io.EOF {
						elog.Info("tun may be closed")
					} else {
						elog.Error("tun write error", err)
					}
					return
				}
			}
		}
	}
}

func (t *TunIO) Enqueue(pkt []byte) {

	if t.IsClosed() {
		elog.Error("tun device have been closed")
		return
	}

	if t.wch != nil {
		t.wch <- pkt
	}
}
