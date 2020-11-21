package polevpnmobile

import (
	"sync"

	"github.com/polevpn/anyvalue"
	"github.com/polevpn/elog"
	"github.com/polevpn/poleclient/core"
)

const (
	POLEVPN_MOBILE_INIT     = 0
	POLEVPN_MOBILE_STARTED  = 1
	POLEVPN_MOBILE_STOPPED  = 2
	POLEVPN_MOBILE_STARTING = 3
	POLEVPN_MOBILE_STOPPING = 4
)

type StartCallback interface {
	OnEvent()
}
type StopCallback interface {
	OnEvent()
}
type ErrorCallback interface {
	OnEvent(msg string)
}
type AddressAllocCallback interface {
	OnEvent(ip string, dns string)
}
type ReconnectingCallback interface {
	OnEvent()
}
type ReconnectedCallback interface {
	OnEvent()
}

type PoleVPN struct {
	startCb        StartCallback
	stopCb         StopCallback
	errCb          ErrorCallback
	reconnectingCb ReconnectingCallback
	reconnectedCb  ReconnectedCallback
	addressAllocCb AddressAllocCallback
	client         *core.PoleVpnClient
	mutex          *sync.Mutex
	mode           bool
	state          int
}

func NewPoleVPN() (*PoleVPN, error) {
	client, err := core.NewPoleVpnClient()
	if err != nil {
		return nil, err
	}
	return &PoleVPN{client: client, mutex: &sync.Mutex{}, state: POLEVPN_MOBILE_INIT}, nil
}

func (pvm *PoleVPN) eventHandler(event int, client *core.PoleVpnClient, av *anyvalue.AnyValue) {
	switch event {
	case core.CLIENT_EVENT_ADDRESS_ALLOCED:
		{
			if pvm.addressAllocCb != nil {
				pvm.addressAllocCb.OnEvent(av.Get("ip").AsStr(), av.Get("dns").AsStr())
			}
		}
	case core.CLIENT_EVENT_STOPPED:
		{
			if pvm.startCb != nil {
				pvm.stopCb.OnEvent()
			}
			pvm.mutex.Lock()
			defer pvm.mutex.Unlock()
			pvm.state = POLEVPN_MOBILE_STOPPED
		}
	case core.CLIENT_EVENT_RECONNECTED:
		if pvm.reconnectingCb != nil {
			pvm.reconnectingCb.OnEvent()
		}
	case core.CLIENT_EVENT_RECONNECTING:
		if pvm.reconnectingCb != nil {
			pvm.reconnectingCb.OnEvent()
		}
	case core.CLIENT_EVENT_STARTED:
		if pvm.startCb != nil {
			pvm.startCb.OnEvent()
		}
		pvm.mutex.Lock()
		defer pvm.mutex.Unlock()
		pvm.state = POLEVPN_MOBILE_STARTED
	case core.CLIENT_EVENT_ERROR:
		elog.Info("client error", av.Get("error").AsStr())
		if pvm.errCb != nil {
			pvm.errCb.OnEvent(av.Get("error").AsStr())
		}
	default:
		elog.Error("invalid evnet=", event)
	}

}

func (pvm *PoleVPN) Attach(fd int) {
	tundevice := core.NewTunDevice()
	tundevice.Attach(fd)
	pvm.client.AttachTunDevice(tundevice)
}

func (pvm *PoleVPN) Start(endpoint string, user string, pwd string, sni string) {

	pvm.mutex.Lock()
	defer pvm.mutex.Unlock()
	if pvm.state != POLEVPN_MOBILE_INIT {
		return
	}
	pvm.state = POLEVPN_MOBILE_STARTING
	pvm.client.SetEventHandler(pvm.eventHandler)
	go pvm.client.Start(endpoint, user, pwd, sni)
}

func (pvm *PoleVPN) Stop() {
	pvm.mutex.Lock()
	defer pvm.mutex.Unlock()
	if pvm.state == POLEVPN_MOBILE_STARTED {
		pvm.state = POLEVPN_MOBILE_STOPPING
		go pvm.client.Stop()
	}

}

func (pvm *PoleVPN) SetRouteMode(mode bool) {
	pvm.client.SetRouteMode(mode)
}

func (pvm *PoleVPN) SetStartCallback(startCb StartCallback) {
	pvm.startCb = startCb
}

func (pvm *PoleVPN) SetStopCallback(stopCb StopCallback) {
	pvm.stopCb = stopCb
}
func (pvm *PoleVPN) SetErrorCallback(errCb ErrorCallback) {
	pvm.errCb = errCb
}
func (pvm *PoleVPN) SetReconnectingCallback(reconnectingCb ReconnectingCallback) {
	pvm.reconnectingCb = reconnectingCb
}
func (pvm *PoleVPN) SetReconnectedCallback(reconnectedCb ReconnectedCallback) {
	pvm.reconnectedCb = reconnectedCb
}
func (pvm *PoleVPN) SetAddressAllocCallback(addressAllocCb AddressAllocCallback) {
	pvm.addressAllocCb = addressAllocCb
}
