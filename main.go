package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/google/netstack/tcpip"
	"github.com/google/netstack/tcpip/buffer"
	"github.com/google/netstack/tcpip/header"
	"github.com/google/netstack/tcpip/link/channel"
	"github.com/google/netstack/tcpip/network/arp"
	"github.com/google/netstack/tcpip/network/ipv4"
	"github.com/google/netstack/tcpip/stack"
	"github.com/google/netstack/tcpip/transport/icmp"
	"github.com/google/netstack/tcpip/transport/tcp"
	"github.com/google/netstack/tcpip/transport/udp"
	"github.com/google/netstack/waiter"
	"github.com/songgao/water"
	"golang.org/x/net/dns/dnsmessage"
)

func tcpRead(wq *waiter.Queue, ep tcpip.Endpoint, conn net.Conn) {
	defer ep.Close()
	defer conn.Close()
	// Create wait queue entry that notifies a channel.
	waitEntry, notifyCh := waiter.NewChannelEntry(nil)

	wq.EventRegister(&waitEntry, waiter.EventIn)
	defer wq.EventUnregister(&waitEntry)

	for {
		v, _, err := ep.Read(nil)
		if err != nil {
			if err == tcpip.ErrWouldBlock {
				<-notifyCh
				continue
			}

			return
		}
		_, err1 := conn.Write(v)
		if err1 != nil {
			fmt.Println("conn write error", err1)
			return
		}
		//ep.Write(tcpip.SlicePayload(v), tcpip.WriteOptions{})
	}
}

func tcpWrite(wq *waiter.Queue, ep tcpip.Endpoint, conn net.Conn) {
	defer ep.Close()
	defer conn.Close()

	var buf []byte = make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			fmt.Println("conn read error", err)
			break
		}

		ep.Write(tcpip.SlicePayload(buf[:n]), tcpip.WriteOptions{})
		//ep.Write(tcpip.SlicePayload(v), tcpip.WriteOptions{})
	}
}

var ifce *water.Interface
var linkEP *channel.Endpoint
var natMap map[string]string
var localIp string
var wq waiter.Queue

func createTunInterface(ip string) (*water.Interface, error) {
	config := water.Config{
		DeviceType: water.TUN,
	}
	var err error
	ifce, err = water.New(config)
	if err != nil {
		return nil, err
	}

	_, err = exec.Command("bash", "-c", "sudo ifconfig "+ifce.Name()+" 10.7.0.4 10.7.0.5 up").Output()

	if err != nil {
		return nil, err
	}

	_, err = exec.Command("bash", "-c", "sudo route -n add -net 10.7.0.0 -netmask 255.255.255.0 10.7.0.5").Output()

	if err != nil {
		return nil, err
	}

	_, err = exec.Command("bash", "-c", "sudo route -n add -net "+ip+" -netmask 255.255.255.255 10.7.0.5").Output()

	if err != nil {
		return nil, err
	}
	return ifce, nil
}

func tunRead() {
	var pkg []byte = make([]byte, 65535)

	for {

		n, err := ifce.Read(pkg)
		if err != nil {
			fmt.Println(err)
			break
		}
		pkg1 := pkg[:n]
		ipv4pkg := header.IPv4(pkg1)
		if ipv4pkg.Protocol() == uint8(icmp.ProtocolNumber4) {
			icmppkg := header.ICMPv4(pkg1[20:])
			fmt.Println("icmp packet", icmppkg.Sequence())
		} else if ipv4pkg.Protocol() == uint8(tcp.ProtocolNumber) {
			tcppkg := header.TCP(pkg1[20:])
			fmt.Println("tcp packet", tcppkg.SourcePort(), tcppkg.DestinationPort())
		} else if ipv4pkg.Protocol() == uint8(udp.ProtocolNumber) {
			udppkg := header.UDP(pkg1[20:])
			fmt.Println("udp packet", udppkg.SourcePort(), udppkg.DestinationPort())

			if udppkg.DestinationPort() == 53 {
				var msg dnsmessage.Message
				err = msg.Unpack(pkg1[28:])
				if err != nil {
					fmt.Println("parser dns packet fail", err)
				} else {
					for i := 0; i < len(msg.Questions); i++ {
						fmt.Println(msg.Questions[i].Name.String(), msg.Questions[i].Type)
					}
				}
			}
		} else {
			fmt.Println("unknown packet")
		}
		pkgBuffer := tcpip.PacketBuffer{Data: buffer.NewViewFromBytes(pkg1).ToVectorisedView()}
		linkEP.InjectInbound(ipv4.ProtocolNumber, pkgBuffer)
		if err != nil {
			fmt.Println("websocket write fail", err)
			break
		}
	}
}

func tunWrite() {

	for {
		pkgInfo := <-linkEP.C
		view := buffer.NewVectorisedView(1, []buffer.View{pkgInfo.Pkt.Header.View()})
		view.Append(pkgInfo.Pkt.Data)
		fmt.Printf("proto=%v,pkg=%x\n", pkgInfo.Proto, view.ToView())
		_, err := ifce.Write([]byte(view.ToView()))
		if err != nil {
			fmt.Println("write tun error", err)
		}
	}
}

func forwardTCP(r *tcp.ForwarderRequest) {

	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		fmt.Println("create tcp endpint error", err)
		return
	}

	addr, _ := ep.GetLocalAddress()
	laddr, _ := net.ResolveTCPAddr("tcp4", localIp+":0")
	raddr, _ := net.ResolveTCPAddr("tcp4", addr.Addr.String()+":"+strconv.Itoa(int(addr.Port)))

	conn, err1 := net.DialTCP("tcp4", laddr, raddr)
	if err1 != nil {
		log.Println("conn dial error ", err1)
		ep.Close()
		return
	}
	go tcpRead(&wq, ep, conn)
	go tcpWrite(&wq, ep, conn)
}

var udppkg []byte = make([]byte, 65535)

func forwardUDP(r *udp.ForwarderRequest) {

	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		fmt.Println("create endpint error", err)
		return
	}

	defer ep.Close()
	var addr tcpip.FullAddress
	v, _, err := ep.Read(&addr)
	if err != nil {
		fmt.Println("read from udp endpoint fail", err)
		return
	}

	laddr, _ := net.ResolveUDPAddr("udp4", localIp+":0")
	raddr, _ := net.ResolveUDPAddr("udp4", r.ID().LocalAddress.To4().String()+":"+strconv.Itoa(int(r.ID().LocalPort)))

	conn, err1 := net.DialUDP("udp4", laddr, raddr)
	if err1 != nil {
		fmt.Println("udp conn dial error ", err1)
		return
	}
	defer conn.Close()

	var n int
	n, err1 = conn.Write(v)

	if err1 != nil {
		fmt.Println("udp conn writeto error ", err1)
		return
	}

	n, err1 = conn.Read(udppkg)

	if err1 != nil {
		fmt.Println("udp conn readfrom error ", err1)
		return
	}
	udppkg1 := udppkg[:n]
	_, _, err = ep.Write(tcpip.SlicePayload(udppkg1), tcpip.WriteOptions{To: &addr})
	if err != nil {
		fmt.Println("udp ep write data fail ", err)
		return
	}
}

func main() {
	flag.Parse()
	if len(flag.Args()) != 1 {
		log.Fatal("Usage: ", os.Args[0], "forward_ip")
	}
	forwardIp := flag.Arg(0)

	natMap = make(map[string]string)

	var err error

	localIp, err = GetLocalIp()

	if err != nil {
		log.Fatal("get localip fail", err)
	}

	fmt.Println("local ip ", localIp)

	rand.Seed(time.Now().UnixNano())

	// Parse the mac address.
	maddr, err := net.ParseMAC("01:01:01:01:01:01")
	if err != nil {
		log.Fatalf("Bad MAC address")
	}

	// Create the stack with ip and tcp protocols, then add a tun-based
	// NIC and address.
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocol{ipv4.NewProtocol(), arp.NewProtocol()},
		TransportProtocols: []stack.TransportProtocol{tcp.NewProtocol(), udp.NewProtocol()},
	})

	linkEP = channel.New(100, 1500, tcpip.LinkAddress(maddr))

	if err := s.CreateNIC(1, linkEP); err != nil {
		log.Fatal(err)
	}

	subnet1, err := tcpip.NewSubnet(tcpip.Address(net.IPv4(0, 0, 0, 0).To4()), tcpip.AddressMask(net.IPv4Mask(0, 0, 0, 0)))
	if err != nil {
		log.Fatal(err)
	}

	if err := s.AddAddressRange(1, ipv4.ProtocolNumber, subnet1); err != nil {
		log.Fatal(err)
	}

	if err := s.AddAddress(1, arp.ProtocolNumber, arp.ProtocolAddress); err != nil {
		log.Fatal(err)
	}

	subnet, err := tcpip.NewSubnet(tcpip.Address(net.IPv4(0, 0, 0, 0).To4()), tcpip.AddressMask(net.IPv4Mask(0, 0, 0, 0)))
	if err != nil {
		log.Fatal(err)
	}
	// Add default route.
	s.SetRouteTable([]tcpip.Route{
		{
			Destination: subnet,
			NIC:         1,
		},
	})

	uf := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		go forwardUDP(r)
	})

	s.SetTransportProtocolHandler(udp.ProtocolNumber, uf.HandlePacket)

	tf := tcp.NewForwarder(s, 0, 10, func(r *tcp.ForwarderRequest) {
		go forwardTCP(r)
	})

	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)

	ifce, err = createTunInterface(forwardIp)

	if err != nil {
		log.Fatal(err)
	}

	go tunRead()
	go tunWrite()

	time.Sleep(time.Hour)

}
