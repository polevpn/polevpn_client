package core

import (
	"io"
	"strings"

	"github.com/polevpn/elog"
)

const (
	IP4_HEADER_LEN = 20
	TCP_HEADER_LEN = 20
	UDP_HEADER_LEN = 8
	DNS_PORT       = 53
	MTU            = 1500
)

var (
	mode = false
)

type TunIO struct {
	device  *TunDevice
	wch     chan []byte
	mtu     int
	handler func(pkt []byte)
	closed  bool
}

func NewTunIO(size int) *TunIO {

	return &TunIO{
		wch:    make(chan []byte, size),
		closed: false,
		mtu:    MTU,
	}
}

func (t *TunIO) SetPacketHandler(handler func(pkt []byte)) {
	t.handler = handler
}

func (t *TunIO) AttachDevice(device *TunDevice) {
	t.device = device
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
	return t.device.Close()
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
		n, err := t.device.GetInterface().Read(pkt)
		if err != nil {
			if err == io.EOF || strings.Index(err.Error(), "file already closed") > -1 {
				elog.Info("tun device closed")
			} else {
				elog.Error("read pkg from tun fail", err)
			}
			return
		}
		pkt = pkt[:n]
		if t.handler != nil {
			t.handler(pkt)
		}
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
				_, err := t.device.GetInterface().Write(pkt)
				if err != nil {
					if err == io.EOF || strings.Index(err.Error(), "file already closed") > -1 {
						elog.Info("tun device closed")
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
		elog.Debug("tun device have been closed")
		return
	}

	if t.wch != nil {
		t.wch <- pkt
	}
}
