package core

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/polevpn/elog"
)

const (
	CH_HTTP_WRITE_SIZE     = 100
	HTTP_HANDSHAKE_TIMEOUT = 5
)

type HttpConn struct {
	streamId string
	endpoint string
	writer   *io.PipeWriter
	reader   io.ReadCloser
	wch      chan []byte
	closed   bool
	handler  map[uint16]func(PolePacket, Conn)
	mutex    *sync.Mutex
	localip  string
}

func NewHttpConn() *HttpConn {
	return &HttpConn{
		closed:  true,
		wch:     nil,
		handler: make(map[uint16]func(PolePacket, Conn)),
		mutex:   &sync.Mutex{},
	}
}

func (hc *HttpConn) SetLocalIP(ip string) {
	hc.localip = ip
}

func (hc *HttpConn) Connect(endpoint string, user string, pwd string, ip string) error {

	localip := hc.localip
	var err error
	var laddr *net.TCPAddr
	if localip != "" {
		laddr, _ = net.ResolveTCPAddr("tcp4", localip+":0")
	}

	var d *http.Client

	d = &http.Client{
		Transport: &http.Transport{
			DialContext:     (&net.Dialer{LocalAddr: laddr}).DialContext,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	req, err := http.NewRequest("PUT", endpoint+"?user="+url.QueryEscape(user)+"&pwd="+url.QueryEscape(pwd)+"&ip="+ip, nil)
	if err != nil {
		plog.Error("http connect fail,", err)
		return ErrNetwork
	}
	resp, err := d.Do(req)
	if err != nil {
		plog.Error("http connect fail,", err)
		return ErrNetwork
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusBadRequest {
			return ErrIPNotExist
		} else if resp.StatusCode == http.StatusForbidden {
			return ErrLoginVerify
		} else {
			return ErrConnectUnknown
		}
	}

	data, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		plog.Error("http connect fail,", err)
		return ErrNetwork
	}

	resp.Body.Close()
	streamId := string(data)

	reader, writer := io.Pipe()

	go func() {
		req, err = http.NewRequest("POST", endpoint+"?stream="+streamId, reader)
		if err != nil {
			plog.Error("http connect fail,", err)
			writer.Close()
			return
		}
		_, err = d.Do(req)
		if err != nil {
			plog.Error("http connect fail,", err)
		}
	}()

	req, err = http.NewRequest("GET", endpoint+"?stream="+streamId, nil)
	if err != nil {
		plog.Error("http connect fail,", err)
		writer.Close()
		return ErrNetwork
	}

	resp, err = d.Do(req)
	if err != nil {
		plog.Error("http connect fail,", err)
		writer.Close()
		return ErrNetwork
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		writer.Close()
		plog.Error("http connect fail,status code=", resp.StatusCode)
		return ErrNetwork
	}
	hc.mutex.Lock()
	defer hc.mutex.Unlock()
	hc.writer = writer
	hc.reader = resp.Body
	hc.streamId = streamId
	hc.endpoint = endpoint
	hc.wch = make(chan []byte, CH_HTTP_WRITE_SIZE)
	hc.closed = false
	return nil
}

func (hc *HttpConn) Close(flag bool) error {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()

	if hc.closed == false {
		hc.closed = true
		if hc.wch != nil {
			hc.wch <- nil
			close(hc.wch)
		}
		hc.writer.Close()
		err := hc.reader.Close()
		if flag {
			pkt := make([]byte, POLE_PACKET_HEADER_LEN)
			PolePacket(pkt).SetCmd(CMD_CLIENT_CLOSED)
			go hc.dispatch(pkt)
		}
		return err
	}
	return nil
}

func (hc *HttpConn) String() string {
	return hc.streamId
}

func (hc *HttpConn) IsClosed() bool {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()

	return hc.closed
}

func (hc *HttpConn) SetHandler(cmd uint16, handler func(PolePacket, Conn)) {
	hc.handler[cmd] = handler
}

func (hc *HttpConn) read() {
	defer func() {
		hc.Close(true)
	}()

	defer PanicHandler()

	for {
		prefetch := make([]byte, 2)
		_, err := hc.reader.Read(prefetch)
		if err != nil {
			if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
				elog.Info(hc.String(), "conn closed")
			} else {
				elog.Error(hc.String(), "conn read exception:", err)
			}
			return
		}

		len := binary.BigEndian.Uint16(prefetch)

		pkt := make([]byte, len)
		copy(pkt, prefetch)
		var offset uint16 = 2
		for {
			n, err := hc.reader.Read(pkt[offset:])
			if err != nil {
				if err == io.EOF || strings.Index(err.Error(), "use of closed network connection") > -1 {
					elog.Info(hc.String(), "conn closed")
				} else {
					elog.Error(hc.String(), "conn read exception:", err)
				}
				return
			}
			offset += uint16(n)
			if offset >= len {
				break
			}
		}

		hc.dispatch(pkt)

	}

}

func (hc *HttpConn) dispatch(pkt []byte) {
	ppkt := PolePacket(pkt)

	handler, ok := hc.handler[ppkt.Cmd()]
	if ok {
		handler(pkt, hc)
	} else {
		plog.Error("invalid pkt cmd=", ppkt.Cmd())
	}
}

func (hc *HttpConn) write() {
	defer PanicHandler()

	for {
		select {
		case pkt, ok := <-hc.wch:
			if !ok {
				plog.Error("get pkt from write channel fail,maybe channel closed")
				return
			} else {
				if pkt == nil {
					plog.Info("exit write process")
					return
				}
				_, err := hc.writer.Write(pkt)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						plog.Info(hc.String(), "conn closed")
					} else {
						plog.Error(hc.String(), "conn write exception:", err)
					}
					return
				}
			}
		}
	}
}

func (hc *HttpConn) Send(pkt []byte) {
	if hc.IsClosed() == true {
		plog.Debug("websocket connection is closed,can't send pkt")
		return
	}
	if hc.wch != nil {
		hc.wch <- pkt
	}
}

func (hc *HttpConn) StartProcess() {
	go hc.read()
	go hc.write()
}
