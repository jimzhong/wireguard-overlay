package wg

import (
	"crypto/sha256"
	"net"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// State holds the configured state of a Wesher Wireguard interface
type State struct {
	iface          string
	client         *wgctrl.Client
	OverlayNetwork net.IPNet
	OverlayAddr    net.IPNet
	port           int
	privateKey     wgtypes.Key
	PublicKey      wgtypes.Key
}

type Peer struct {
	IP                string
	Port              int
	PublicKey         wgtypes.Key
	PresharedKey      wgtypes.Key
	KeepaliveInterval time.Duration
}

func (p *Peer) toPeerConfig(overlayNet net.IPNet) wgtypes.PeerConfig {
	config := wgtypes.PeerConfig{
		PublicKey: p.PublicKey,
		AllowedIPs: []net.IPNet{
			getOverlayAddr(overlayNet, p.PublicKey),
		},
		PresharedKey: &p.PresharedKey,
	}
	if p.Port != 0 && p.IP != "" {
		config.Endpoint = &net.UDPAddr{IP: net.ParseIP(p.IP), Port: p.Port}
	}
	if p.KeepaliveInterval != 0 {
		config.PersistentKeepaliveInterval = &p.KeepaliveInterval
	}
	return config
}

// GetOverlayAddr synthesizes an address by hashing the pubkey
func getOverlayAddr(ipnet net.IPNet, pubkey wgtypes.Key) net.IPNet {
	// TODO: handle all zero and all one host addresses.
	bits, size := ipnet.Mask.Size()
	ip := make([]byte, len(ipnet.IP))
	copy(ip, []byte(ipnet.IP))
	hb := sha256.Sum256(pubkey[:])
	for i := 1; i <= (size-bits)/8; i++ {
		ip[len(ip)-i] = hb[len(hb)-i]
	}
	return net.IPNet{
		IP:   net.IP(ip),
		Mask: net.CIDRMask(size, size), // either /32 or /128, depending if ipv4 or ipv6
	}
}

// New creates a new Wesher Wireguard state
// The Wireguard keys are generated for every new interface
// The interface must later be setup using SetUpInterface
func New(iface string, port int, overlayNet net.IPNet, privKey string) (*State, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, errors.Wrap(err, "Could not instantiate wireguard client")
	}

	privateKey, err := wgtypes.ParseKey(privKey)
	if err != nil {
		return nil, errors.Wrap(err, "Could not parse private key")
	}
	pubKey := privateKey.PublicKey()
	state := State{
		iface:          iface,
		client:         client,
		privateKey:     privateKey,
		PublicKey:      pubKey,
		OverlayNetwork: overlayNet,
		OverlayAddr:    getOverlayAddr(overlayNet, pubKey),
		port:           port,
	}
	return &state, nil
}

func (s *State) GetOverlayAddress(pubkey wgtypes.Key) net.IPNet {
	return getOverlayAddr(s.OverlayNetwork, pubkey)
}

// DownInterface shuts down the associated network interface
func (s *State) DownInterface() error {
	if _, err := s.client.Device(s.iface); err != nil {
		if os.IsNotExist(err) {
			return nil // device already gone; noop
		}
		return err
	}
	link, err := netlink.LinkByName(s.iface)
	if err != nil {
		return err
	}
	return netlink.LinkDel(link)
}

// SetUpInterface creates and sets up the associated network interface
func (s *State) SetUpInterface() error {
	if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: s.iface}}); err != nil {
		return errors.Wrapf(err, "Could not create interface %s", s.iface)
	}

	if err := s.client.ConfigureDevice(s.iface, wgtypes.Config{
		PrivateKey: &s.privateKey,
		ListenPort: func() *int {
			if s.port == 0 {
				return nil
			}
			return &s.port
		}(),
	}); err != nil {
		return errors.Wrapf(err, "Could not set wireguard configuration for %s", s.iface)
	}

	link, err := netlink.LinkByName(s.iface)
	if err != nil {
		return errors.Wrapf(err, "Could not get link information for %s", s.iface)
	}
	if err := netlink.AddrReplace(link, &netlink.Addr{
		IPNet: &s.OverlayAddr,
	}); err != nil {
		return errors.Wrapf(err, "Could not set address for %s", s.iface)
	}
	// TODO: make MTU configurable?
	if err := netlink.LinkSetMTU(link, 1280); err != nil {
		return errors.Wrapf(err, "Could not set MTU for %s", s.iface)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return errors.Wrapf(err, "Could not enable interface %s", s.iface)
	}

	netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &s.OverlayNetwork,
		Scope:     netlink.SCOPE_LINK,
	})
	return nil
}

func (s *State) AddPeers(peers []Peer) error {
	config := make([]wgtypes.PeerConfig, 0, len(peers))
	for _, p := range peers {
		if p.PublicKey != s.PublicKey {
			config = append(config, p.toPeerConfig(s.OverlayNetwork))
		}
	}
	if err := s.client.ConfigureDevice(s.iface, wgtypes.Config{
		Peers: config,
	}); err != nil {
		return errors.Wrapf(err, "Could not set peers for %s", s.iface)
	}
	return nil
}

func fromWgtypesPeer(p *wgtypes.Peer) Peer {
	peer := Peer{
		PublicKey:         p.PublicKey,
		PresharedKey:      p.PresharedKey,
		KeepaliveInterval: p.PersistentKeepaliveInterval,
	}
	if p.Endpoint != nil {
		peer.IP = p.Endpoint.IP.String()
		peer.Port = p.Endpoint.Port
	}
	return peer
}

func (s *State) GetPeers() ([]Peer, error) {
	device, err := s.client.Device(s.iface)
	if err != nil {
		return nil, err
	}
	peers := make([]Peer, 0, len(device.Peers))
	for _, p := range device.Peers {
		peers = append(peers, fromWgtypesPeer(&p))
	}
	return peers, nil
}
