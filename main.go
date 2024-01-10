package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	mrand "math/rand"

	"github.com/ipfs/go-log/v2"
	"github.com/multiformats/go-multiaddr"

	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	rh "github.com/libp2p/go-libp2p/p2p/host/routed"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	rcon "github.com/vishalpatidar99/bootstrap-node/config"
	rutil "github.com/vishalpatidar99/bootstrap-node/util"
)

var kadPrefix = dht.ProtocolPrefix("/rocket")

const customNamespace = "/rocket-dht/"

type blankValidator struct{}

func (blankValidator) Validate(key string, value []byte) error {
	namespacedKey := customNamespace + key

	// Check if the key has the correct namespace
	if !strings.HasPrefix(namespacedKey, customNamespace) {
		return errors.New("invalid key namespace")
	}

	// Validate the value based on your requirements
	// For example, check if the value is not empty
	if len(value) == 0 {
		return errors.New("value cannot be empty")
	}
	return nil
}
func (blankValidator) Select(_ string, _ [][]byte) (int, error) { return 0, nil }

var logger = log.Logger("rocket-bootstrap-node")

func main() {
	configFilePath := flag.String("config", "", "json configuration file; empty uses the default configuration")
	flag.Parse()
	config, err := rcon.LoadConfig(*configFilePath)
	if err != nil {
		panic(err)
	}

	log.SetAllLoggers(log.LevelInfo)
	log.SetLogLevel("rendezvous", "info")

	var prvKey crypto.PrivKey
	var pubKey crypto.PubKey

	// try to read from file
	pubKeyFilebuffer, _ := os.ReadFile(config.PubKeyFilePath)
	privKeyFilebuffer, err := os.ReadFile(config.PrivKeyFilePath)
	if err != nil {
		logger.Info("No Existing Key - Creating A New One")
		r := mrand.New(mrand.NewSource(time.Now().Unix()))
		prvKey, pubKey, err = crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
		if err != nil {
			panic(err)
		}

		f, err := os.Create(config.PrivKeyFilePath)
		if err != nil {
			logger.Error("Couldn't Create File: " + err.Error())
		}
		prvKeyBytes, _ := crypto.MarshalPrivateKey(prvKey)
		_, err = f.Write(prvKeyBytes)
		if err != nil {
			logger.Error("Couldn't Write to File: " + err.Error())
		}
		f.Sync()

		f, err = os.Create(config.PubKeyFilePath)
		if err != nil {
			logger.Error("Couldn't Create File: " + err.Error())
		}
		pubKeyBytes, _ := crypto.MarshalPublicKey(pubKey)

		_, err = f.Write(pubKeyBytes)
		if err != nil {
			logger.Error("Couldn't Write to File: " + err.Error())
		}

		f.Sync()
	} else {
		prvKey, err = crypto.UnmarshalPrivateKey(privKeyFilebuffer)
		if err != nil {
			logger.Error("Couldn't Unmarshall Private Key: " + err.Error())
		}

		pubKey, err = crypto.UnmarshalPublicKey(pubKeyFilebuffer)
		if err != nil {
			logger.Error("Couldn't Unmarshall Public Key: " + err.Error())
		}
	}
	connmgr, err := connmgr.NewConnManager(
		100, // Lowwater
		400, // HighWater,
		connmgr.WithGracePeriod(time.Minute),
	)
	if err != nil {
		logger.Errorf("Error Creating Connection Manager: %v", err)
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(
			config.ListenAddrs...),
		libp2p.Identity(prvKey),
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.ConnectionManager(connmgr),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
	}

	host, err := libp2p.New(opts...)
	//host, err := libp2p.New(libp2p.ListenAddrs([]multiaddr.Multiaddr(nil)...))
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	dstore := dsync.MutexWrap(ds.NewMapDatastore())
	baseOpts := []dht.Option{
		kadPrefix,
		dht.Datastore(dstore),
		dht.NamespacedValidator("rocket-dht", blankValidator{}),
		dht.Mode(dht.ModeAutoServer),
	}
	kdht, err := dht.New(ctx, host, baseOpts...)
	if err != nil {
		panic(err)
	}
	routedH := rh.Wrap(host, kdht)

	if len(config.BootstrapPeers) > 0 {
		logger.Info("Bootstrapping the DHT")

		bootstrapPeers := rutil.ConvertPeers(config.BootstrapPeers)

		err = bootstrap(ctx, routedH, bootstrapPeers)
		if err != nil {
			logger.Errorf("error bootstrapping: %v", err)
		}
	}

	hostAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s", routedH.ID().String()))
	rawPubKey, _ := pubKey.Raw()
	fmt.Println("Host created. ID: ", host.ID().String())
	fmt.Printf("Host Public Key: %x\n", rawPubKey)
	fmt.Println("Listening Addresses: ")
	for _, addr := range host.Addrs() {
		fmt.Println(addr.Encapsulate(hostAddr))
	}

	select {}

}

// This code is borrowed from the go-ipfs bootstrap process
func bootstrap(ctx context.Context, ph host.Host, peers []peer.AddrInfo) error {
	if len(peers) < 1 {
		return fmt.Errorf("not bootstrap peers")
	}

	errs := make(chan error, len(peers))
	var wg sync.WaitGroup
	for _, p := range peers {

		wg.Add(1)
		go func(p peer.AddrInfo) {
			defer wg.Done()
			logger.Info("bootstrap: connecting to ", p.ID)

			ph.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)
			if err := ph.Connect(ctx, p); err != nil {
				logger.Errorf("bootstrap connection to %v failed - error: %v", p.ID, err)
				errs <- err
				return
			}
			logger.Infof("successful bootstrap connection to %v", p.ID)
		}(p)
	}
	wg.Wait()

	// our failure condition is when no connection attempt succeeded.
	// So drain the errs channel, counting the results.
	close(errs)
	count := 0
	var err error
	for err = range errs {
		if err != nil {
			count++
		}
	}
	if count == len(peers) {
		return fmt.Errorf("failed to bootstrap. %s", err)
	}
	return nil
}
