package main

import (
	"encoding/gob"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jimzhong/wireguard-mesh/internal/config"
	"github.com/jimzhong/wireguard-mesh/internal/wg"
	"github.com/sirupsen/logrus"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func fetchPeers(server net.TCPAddr) ([]wg.Peer, error) {
	client := &http.Client{
		Timeout: 11 * time.Second,
	}
	url := url.URL{
		Scheme: "http",
		Host:   server.String(),
		Path:   "/",
	}
	logrus.Debug("Fetching peers from ", url.String())
	res, err := client.Get(url.String())
	if err != nil {
		logrus.WithError(err).Error("Could not connect to server")
		return nil, err
	}
	defer res.Body.Close()
	var peers []wg.Peer
	if err := gob.NewDecoder(res.Body).Decode(&peers); err != nil {
		logrus.WithError(err).Error("Could not decode peer list")
		return nil, err
	}
	logrus.Debug("Fetched peers: ", peers)
	return peers, nil
}

func updatePeers(wg *wg.State, serverAddr net.TCPAddr, preshardKey wgtypes.Key, timer chan<- *time.Timer) {
	peers, err := fetchPeers(serverAddr)
	if err == nil {
		for i := range peers {
			peers[i].PresharedKey = preshardKey
		}
		err = wg.AddPeers(peers)
		if err != nil {
			logrus.WithError(err).Error("Could not add peers")
		}
		logrus.Debug("Added peers: ", peers)
	}
	timer <- time.NewTimer(time.Second * time.Duration(rand.Int()%20+20))
}

func main() {
	config, err := config.LoadClientConfig()
	if err != nil {
		logrus.Fatal(err)
	}
	logLevel, err := logrus.ParseLevel(config.LogLevel)
	if err != nil {
		logrus.WithError(err).Fatal("Could not parse loglevel")
	}
	logrus.SetLevel(logLevel)

	serverPubkey, err := wgtypes.ParseKey(config.ServerPubkey)
	if err != nil {
		logrus.WithError(err).Fatal("Could not parse server key")
	}
	presharedKey := func() wgtypes.Key {
		if config.PresharedKey != "" {
			key, err := wgtypes.ParseKey(config.PresharedKey)
			if err != nil {
				logrus.WithError(err).Fatal("Could not parse preshared key")
			}
			return key
		}
		return wgtypes.Key{}
	}()

	wgState, err := wg.New(config.Interface, 0, (*net.IPNet)(config.OverlayNet), config.PrivateKey)
	if err != nil {
		logrus.WithError(err).Fatal("Could not instantiate wireguard controller")
	}
	if err := wgState.SetUpInterface(); err != nil {
		logrus.WithError(err).Fatal("Could not up interface")
	}
	defer func() {
		logrus.Info("Exiting...")
		if err := wgState.DownInterface(); err != nil {
			logrus.WithError(err).Error("Could not down interface")
		}
	}()

	if err := wgState.AddPeers([]wg.Peer{
		{
			PublicKey: serverPubkey,
			IP:        config.ServerAddr.String(),
			Port:      config.ServerPort,
		},
	}); err != nil {
		logrus.WithError(err).Fatal("Could not add server as wireguard peer")
	}

	logrus.Infof("Client is running. Pubkey: %s IP: %s", wgState.PublicKey, &wgState.OverlayAddr)
	incomingSignals := make(chan os.Signal, 1)
	signal.Notify(incomingSignals, syscall.SIGTERM, os.Interrupt)
	timer := time.NewTimer(0)
	resp := make(chan *time.Timer)

main_loop:
	for {
		select {
		case <-incomingSignals:
			break main_loop
		case <-timer.C:
			go updatePeers(wgState, net.TCPAddr{IP: wgState.GetOverlayAddress(serverPubkey).IP, Port: config.ServerPort}, presharedKey, resp)
		case timer = <-resp:
			logrus.Debug("Got new timer")
		}
	}
}