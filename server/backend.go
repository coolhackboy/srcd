package server

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/srchain/srcd/common/common"
	"github.com/srchain/srcd/common/hexutil"
	"github.com/srchain/srcd/consensus"
	"github.com/srchain/srcd/consensus/pow"
	"github.com/srchain/srcd/core/blockchain"
	"github.com/srchain/srcd/core/mempool"
	"github.com/srchain/srcd/database"
	"github.com/srchain/srcd/log"
	"github.com/srchain/srcd/miner"
	"github.com/srchain/srcd/node"
	"github.com/srchain/srcd/params"
	"github.com/srchain/srcd/rlp"
	"github.com/srchain/srcd/p2p"

	"github.com/srchain/srcd/account"

	"github.com/srchain/srcd/accounts"
)

// SilkRoad implements the full node service.
type SilkRoad struct {
	config *Config
	// chainConfig *params.ChainConfig

	// Channel for shutting down the service
	// shutdownChan chan bool

	// Handlers
	txPool          *mempool.TxPool
	blockchain      *blockchain.BlockChain
	protocolManager *ProtocolManager

	// DB interfaces
	chainDb database.Database // Block chain database

	// eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *account.AccountManager

	// bloomRequests chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	// bloomIndexer  *core.ChainIndexer             // Bloom indexer operating during block imports

	// APIBackend *EthAPIBackend

	miner    *miner.Miner
	coinbase common.Address

	networkID     uint64
	// netRPCService *ethapi.PublicNetAPI

	lock sync.RWMutex
}

// New creates a new SilkRoad object
func New(ctx *node.ServiceContext, config *Config) (*SilkRoad, error) {
	chainDb, err := CreateDB(ctx, config, "chaindata")
	if err != nil {
		return nil, err
	}

	silk := &SilkRoad{
		config:         config,
		chainDb:        chainDb,
		accountManager: ctx.AccountManager,
		engine:         CreateConsensusEngine(),
		// shutdownChan:   make(chan bool),
		coinbase:       config.Coinbase,
	}

	if _, genesisErr := blockchain.SetupGenesisBlock(chainDb, config.Genesis); genesisErr != nil {
		return nil, genesisErr
	}
	silk.blockchain, err = blockchain.NewBlockChain(chainDb, silk.engine)
	if err != nil {
		return nil, err
	}

	// silk.bloomIndexer.Start(eth.blockchain)

	// if config.TxPool.Journal != "" {
	// config.TxPool.Journal = ctx.ResolvePath(config.TxPool.Journal)
	// }
	silk.txPool = mempool.NewTxPool(config.TxPool, silk.blockchain)

	if silk.protocolManager, err = NewProtocolManager(silk.chainConfig, config.SyncMode, config.NetworkId, silk.eventMux, silk.txPool, silk.engine, silk.blockchain, chainDb); err != nil {
		return nil, err
	}

	silk.miner = miner.New(silk, silk.engine)
	silk.miner.SetExtra(makeExtraData(config.ExtraData))

	return silk, nil
}

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		// create default extradata
		extra, _ = rlp.EncodeToBytes([]interface{}{
			uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch),
			"srcd",
			runtime.Version(),
			runtime.GOOS,
		})
	}
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		log.Warn("Miner extra data exceed limit", "extra", hexutil.Bytes(extra), "limit", params.MaximumExtraDataSize)
		extra = nil
	}
	return extra
}

// CreateDB creates the chain database.
func CreateDB(ctx *node.ServiceContext, config *Config, name string) (database.Database, error) {
	return ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
}

// CreateConsensusEngine creates the required type of consensus engine instance for SilkRoad
func CreateConsensusEngine() consensus.Engine {
	engine := pow.New()
	engine.SetThreads(-1)

	return engine
}

func (s *SilkRoad) Coinbase() (cb common.Address, err error) {
	s.lock.RLock()
	coinbase := s.coinbase
	s.lock.RUnlock()

	if coinbase != (common.Address{}) {
		return coinbase, nil
	}
	//if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
	//	if accounts := wallets[0].Accounts(); len(accounts) > 0 {
	//		coinbase := accounts[0].Address
	//
	//		s.lock.Lock()
	//		s.coinbase = coinbase
	//		s.lock.Unlock()
	//
	//		log.Info("Coinbase automatically configured", "address", coinbase)
	//		return coinbase, nil
	//	}
	//}
	return common.Address{}, fmt.Errorf("coinbase must be explicitly specified")
}

// StartMining starts the miner with the given number of CPU threads. If mining
// is already running, this method adjust the number of threads allowed to use.
func (s *SilkRoad) StartMining(threads int) error {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		log.Info("Updated mining threads", "threads", threads)
		if threads == 0 {
			threads = -1 // Disable the miner from within
		}
		th.SetThreads(threads)
	}
	// If the miner was not running, initialize it
	if !s.IsMining() {
		// Configure the local mining address
		cb, err := s.Coinbase()
		if err != nil {
			log.Error("Cannot start mining without coinbase", "err", err)
			return fmt.Errorf("coinbase missing: %v", err)
		}

		// If mining is started, we can disable the transaction rejection mechanism
		// introduced to speed sync times.
		// atomic.StoreUint32(&s.protocolManager.acceptTxs, 1)

		go s.miner.Start(cb)
	}
	return nil
}

func (s *SilkRoad) IsMining() bool { return s.miner.Mining() }


func (s *SilkRoad) AccountManager() *accounts.Manager  { return s.accountManager }
func (s *SilkRoad) BlockChain() *blockchain.BlockChain { return s.blockchain }
func (s *SilkRoad) TxPool() *mempool.TxPool            { return s.txPool }
func (s *SilkRoad) Engine() consensus.Engine           { return s.engine }
func (s *SilkRoad) ChainDb() database.Database         { return s.chainDb }

//func (s *Server) AccountManager() *accounts.Manager  { return s.accountManager }



// Protocols implements node.Service, returning all the currently configured
// network protocols to start.
func (s *SilkRoad) Protocols() []p2p.Protocol {
	return s.protocolManager.SubProtocols
}

// Start implements node.Service, starting all internal goroutines needed by the

// SilkRoad protocol implementation.
// func (s *SilkRoad) Start(srvr *p2p.SilkRoad) error {
func (s *SilkRoad) Start(server *p2p.Server) error {

	// // Start the RPC service
	// s.netRPCService = ethapi.NewPublicNetAPI(srvr, s.NetVersion())

	// Start the networking layer
	maxPeers := server.MaxPeers
	s.protocolManager.Start(maxPeers)

	return nil
}

// Stop implements node.Service, terminating all internal goroutines used by the
// SilkRoad protocol.
func (s *SilkRoad) Stop() error {
	// s.bloomIndexer.Close()
	s.blockchain.Stop()
	s.protocolManager.Stop()
	// s.txPool.Stop()
	s.miner.Stop()
	// s.eventMux.Stop()

	s.chainDb.Close()
	// close(s.shutdownChan)
	return nil
}
