package node

import (
	"fmt"
)

type ProtocolManager struct {
	networkID uint64

	fastSync  uint32 // Flag whether fast sync is enabled (gets disabled if we already have blocks)
	acceptTxs uint32 // Flag whether we're considered synchronised (enables transaction processing)

	txpool      txPool
	blockchain  *core.BlockChain
	chainconfig *params.ChainConfig
	maxPeers    int

	downloader *downloader.Downloader
	fetcher    *fetcher.Fetcher
	peers      *peerSet

	SubProtocols []p2p.Protocol

	eventMux      *event.TypeMux
	txsCh         chan core.NewTxsEvent
	txsSub        event.Subscription
	minedBlockSub *event.TypeMuxSubscription

	// channels for fetcher, syncer, txsyncLoop
	newPeerCh   chan *peer
	txsyncCh    chan *txsync
	quitSync    chan struct{}
	noMorePeers chan struct{}

	// wait group is used for graceful shutdowns during downloading
	// and processing
	wg sync.WaitGroup
}

// Entity implements the full node service.
type Entity struct {
	config      *Config
	chainConfig *params.ChainConfig

	// Channel for shutting down the service
	shutdownChan chan bool // Channel for shutting down the Ethereum

	// Handlers
	txPool          *core.TxPool
	blockchain      *blockchain.BlockChain
	protocolManager *ProtocolManager
	// lesServer       LesServer

	// DB interfaces
	chainDb ethdb.Database // Block chain database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	// bloomRequests chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	// bloomIndexer  *core.ChainIndexer             // Bloom indexer operating during block imports

	// APIBackend *EthAPIBackend

	miner     *miner.Miner
	coinbase  common.Address

	// networkID     uint64
	// netRPCService *ethapi.PublicNetAPI

	lock sync.RWMutex
}

// New creates a new Entity object
func New(ctx *node.ServiceContext, config *Config) (*Entity, error) {
	chainDb, err := CreateDB(ctx, config, "chaindata")
	if err != nil {
		return nil, err
	}
	chainConfig, genesisHash, genesisErr := blockchain.SetupGenesisBlock(chainDb, config.Genesis)
	if _, ok := genesisErr.(*params.ConfigCompatError); genesisErr != nil && !ok {
		return nil, genesisErr
	}
	log.Info("Initialised chain configuration", "config", chainConfig)

	entity := &Entity{
		config:         config,
		chainDb:        chainDb,
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		accountManager: ctx.AccountManager,
		engine:         CreateConsensusEngine(ctx, &config.Pow, chainConfig, chainDb),
		shutdownChan:   make(chan bool),
		coinbase:       config.Coinbase,
	}

	entity.blockchain, err = blockchain.NewBlockChain(chainDb, entity.chainConfig, entity.engine)
	if err != nil {
		return nil, err
	}
	// Rewind the chain in case of an incompatible config upgrade.
	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log.Warn("Rewinding chain to upgrade configuration", "err", compat)
		entity.blockchain.SetHead(compat.RewindTo)
		rawdb.WriteChainConfig(chainDb, genesisHash, chainConfig)
	}
	entity.bloomIndexer.Start(eth.blockchain)

	if config.TxPool.Journal != "" {
		config.TxPool.Journal = ctx.ResolvePath(config.TxPool.Journal)
	}
	entity.txPool = core.NewTxPool(config.TxPool, eth.chainConfig, eth.blockchain)

	if eth.protocolManager, err = NewProtocolManager(eth.chainConfig, config.SyncMode, config.NetworkId, eth.eventMux, eth.txPool, eth.engine, eth.blockchain, chainDb); err != nil {
		return nil, err
	}
	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine)
	eth.miner.SetExtra(makeExtraData(config.ExtraData))

	eth.APIBackend = &EthAPIBackend{eth, nil}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.GasPrice
	}
	eth.APIBackend.gpo = gasprice.NewOracle(eth.APIBackend, gpoParams)

	return eth, nil
}

// CreateDB creates the chain database.
func CreateDB(ctx *node.ServiceContext, config *Config, name string) (ethdb.Database, error) {
	db, err := ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
	if err != nil {
		return nil, err
	}
	if db, ok := db.(*ethdb.LDBDatabase); ok {
		db.Meter("eth/db/chaindata/")
	}
	return db, nil
}

// CreateConsensusEngine creates the required type of consensus engine instance for an Entity service
func CreateConsensusEngine(ctx *node.ServiceContext, config *pow.Config, chainConfig *params.ChainConfig, db ethdb.Database) consensus.Engine {
	engine := pow.New(pow.Config{
		CacheDir:       ctx.ResolvePath(config.CacheDir),
		CachesInMem:    config.CachesInMem,
		CachesOnDisk:   config.CachesOnDisk,
		DatasetDir:     config.DatasetDir,
		DatasetsInMem:  config.DatasetsInMem,
		DatasetsOnDisk: config.DatasetsOnDisk,
	})
	engine.SetThreads(1)
	return engine
}

func (s *Entity) Coinbase() (cb common.Address, err error) {
	s.lock.RLock()
	coinbase := s.coinbase
	s.lock.RUnlock()

	if coinbase != (common.Address{}) {
		return coinbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			coinbase := accounts[0].Address

			s.lock.Lock()
			s.coinbase = coinbase
			s.lock.Unlock()

			log.Info("Coinbase automatically configured", "address", coinbase)
			return coinbase, nil
		}
	}
	return common.Address{}, fmt.Errorf("coinbase must be explicitly specified")
}

func (s *Entity) StartMining(local bool) error {
	cb, err := s.Coinbase()
	if err != nil {
		log.Error("Cannot start mining without coinbase", "err", err)
		return fmt.Errorf("coinbase missing: %v", err)
	}

	if local {
		// If local (CPU) mining is started, we can disable the transaction rejection
		// mechanism introduced to speed sync times. CPU mining on mainnet is ludicrous
		// so none will ever hit this path, whereas marking sync done on CPU mining
		// will ensure that private networks work in single miner mode too.
		atomic.StoreUint32(&s.protocolManager.acceptTxs, 1)
	}
	go s.miner.Start(cb)
	return nil
}

func (s *Node) Engine() consensus.Engine           { return s.engine }
