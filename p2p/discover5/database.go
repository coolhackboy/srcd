package discover5

import (
	"sync"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"encoding/binary"
	"bytes"
	"os"
	"github.com/srchain/srcd/rlp"
	"fmt"
	"github.com/srchain/srcd/log"
	"github.com/srchain/srcd/crypto/crypto"
	"github.com/syndtr/goleveldb/leveldb/util"
	"time"
	"crypto/rand"
	"github.com/syndtr/goleveldb/leveldb/iterator"
)

var (
	nodeDBNilNodeID      = NodeID{}
	nodeDBNodeExpiration = 24 * time.Hour // Time after which an unseen node should be dropped.
	nodeDBCleanupCycle   = time.Hour
	nodeDBVersionKey = []byte("version") // Version of the database to flush if changes
	nodeDBItemPrefix = []byte("n:")      // Identifier to prefix node entries with


	nodeDBDiscoverRoot          = ":discover"
	nodeDBDiscoverPing          = nodeDBDiscoverRoot + ":lastping"
	nodeDBDiscoverPong          = nodeDBDiscoverRoot + ":lastpong"
	nodeDBDiscoverFindFails     = nodeDBDiscoverRoot + ":findfail"
	nodeDBDiscoverLocalEndpoint = nodeDBDiscoverRoot + ":localendpoint"
	nodeDBTopicRegTickets       = ":tickets"

)





// nodeDB stores all nodes we know about.
type nodeDB struct {
	lvl    *leveldb.DB   // Interface to the database itself
	self   NodeID        // Own node id to prevent adding it into the database
	runner sync.Once     // Ensures we can start at most one expirer
	quit   chan struct{} // Channel to signal the expiring thread to stop
}

func newNodeDB(path string, version int, self NodeID) (*nodeDB, error) {
	if path == "" {
		return newMemoryNodeDB(self)
	}
	return newPersistentNodeDB(path,version,self)
}
func newMemoryNodeDB(self NodeID) (*nodeDB, error) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		return nil, err
	}
	return &nodeDB{
		lvl: db,
		self: self,
		quit: make(chan struct{}),
	}, nil
}

func newPersistentNodeDB(path string, version int , self NodeID) (*nodeDB, error) {
	opts := &opt.Options{OpenFilesCacheCapacity: 5}
	db, err := leveldb.OpenFile(path, opts)
	if _, iscorrupted := err.(*errors.ErrCorrupted); iscorrupted {
		db, err = leveldb.RecoverFile(path,nil)
	}
	if err != nil {
		return nil, err
	}

	currentVer := make([]byte, binary.MaxVarintLen64)
	currentVer = currentVer[:binary.PutVarint(currentVer, int64(version))]

	blob, err := db.Get(nodeDBVersionKey,nil)
	switch err {
	case leveldb.ErrNotFound:
		if err := db.Put(nodeDBVersionKey, currentVer,nil); err != nil {
			db.Close()
			return nil, err
		}
	case nil:
		if !bytes.Equal(blob, currentVer) {
			db.Close()
			if err = os.RemoveAll(path); err != nil {
				return nil, err
			}
			return newPersistentNodeDB(path, version, self)
		}
	}

	return &nodeDB{
		lvl: db,
		self: self,
		quit: make(chan struct{}),
	}, nil

}

func makeKey(id NodeID, field string) []byte {
	if bytes.Equal(id[:], nodeDBNilNodeID[:]) {
		return []byte(field)
	}
}

func splitKey(key []byte) (id NodeID, field string) {
	if !bytes.HasPrefix(key, nodeDBItemPrefix) {
		return  NodeID{}, string(key)
	}

	item := key[len(nodeDBItemPrefix):]
	copy(id[:],item[:len(id)])
	field = string(item[len(id):])
	return id, field
}

func (db *nodeDB) fetchInt64(key []byte) int64 {
	blob, err := db.lvl.Get(key,nil)
	if err != nil {
		return 0
	}
	val, read := binary.Varint(blob)
	if read <= 0 {
		return 0
	}
	return val
}

func (db *nodeDB) storeInt64(key []byte, n int64) error {
	blob := make([]byte, binary.MaxVarintLen64)
	blob = blob[:binary.PutVarint(blob, n )]
	return db.lvl.Put(key, blob,nil)
}


func (db *nodeDB) storeRLP(key []byte, val interface{}) error {
	blob, err := rlp.EncodeToBytes(val)
	if err != nil {
		return err
	}
	return db.lvl.Put(key, blob, nil)
}

func (db *nodeDB) fetchRLP(key []byte, val interface{}) error {
	blob, err := db.lvl.Get(key,nil)
	if err != nil {
		return err
	}
	err = rlp.DecodeBytes(blob, val)
	if err != nil {
		log.Warn(fmt.Sprintf("key &x (%T) %v",key,val, err))
	}
	return err
}

func (db *nodeDB) node(id NodeID) *Node {
	var node Node
	if err := db.fetchRLP(makeKey(id,nodeDBDiscoverRoot), &node); err != nil {
		return nil
	}
	node.sha = crypto.Keccak256Hash(node.ID[:])
	return &node
}

func (db *nodeDB) updateNode(node *Node) error {
	return db.storeRLP(makeKey(node.ID,nodeDBDiscoverRoot), node)
}

func (db *nodeDB) deleteNode(id NodeID) error {
	deleter := db.lvl.NewIterator(util.BytesPrefix(makeKey(id,"")),nil)
	for deleter.Next() {
		if err := db.lvl.Delete(deleter.Key(),nil); err != nil {
			return err
		}
	}
}

func (db *nodeDB) ensureExpirer() {
	db.runner.Do(func() { go db.expirer() })
}
func (db *nodeDB) expirer() {
	tick := time.NewTicker(nodeDBCleanupCycle)
	defer  tick.Stop()
	for {
		select {
			case <- tick.C:
				if err := db.expireNodes(); err != nil {

				}
		}
	}
}
func (db *nodeDB) expireNodes() error {
	threshold := time.Now().Add(-nodeDBNodeExpiration)

	it := db.lvl.NewIterator(nil,nil)
	defer it.Release()
	for it.Next() {
		id, field := splitKey(it.Key())
		if field != nodeDBDiscoverRoot {
			continue
		}

		if !bytes.Equal(id[:],db.self[:]) {
			if seen := db.lastPong(id); seen.After(threshold) {
				continue
			}
		}

		db.deleteNode(id)
	}
	return nil
}


func (db *nodeDB) lastPong(id NodeID) time.Time {
	return time.Unix(db.fetchInt64(makeKey(id,nodeDBDiscoverPong)),0)
}

func (db *nodeDB) lastPing(id NodeID) time.Time {
	return time.Unix(db.fetchInt64(makeKey(id,nodeDBDiscoverPing)),0)
}


// updateLastPing updates the last time we tried contacting a remote node.
func (db *nodeDB) updateLastPing(id NodeID, instance time.Time) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverPing), instance.Unix())
}

// findFails retrieves the number of findnode failures since bonding.
func (db *nodeDB) findFails(id NodeID) int {
	return int(db.fetchInt64(makeKey(id, nodeDBDiscoverFindFails)))
}

// updateFindFails updates the number of findnode failures since bonding.
func (db *nodeDB) updateFindFails(id NodeID, fails int) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverFindFails), int64(fails))
}

func (db *nodeDB) localEndpoint(id NodeID) *rpcEndpoint {
	var ep rpcEndpoint
	if err := db.fetchRLP(makeKey(id,nodeDBDiscoverLocalEndpoint), &ep); err != nil {
		return nil
	}
	return &ep
}

func (db *nodeDB) updateLocalEndpoint(id NodeID, ep *rpcEndpoint) error {
	return db.storeRLP(makeKey(id,nodeDBDiscoverLocalEndpoint), &ep)
}

func (db *nodeDB) querySeeds(n int, maxAge time.Duration) []*Node {
	var (
		now = time.Now()
		nodes = make([]*Node,0,n)
		it 	= db.lvl.NewIterator(nil,nil)
		id	NodeID
	)
	defer it.Release()

	seek:
		for seeks := 0; len(nodes) < n && seeks < n*5; seeks++ {
			ctr := id[0]
			rand.Read(id[:])
			id[0] = ctr + id[0]%16
			it.Seek(makeKey(id,nodeDBDiscoverRoot))
			n := nextNode(it)
			if n == nil {
				id[0] = 0
				continue seek
			}
			if n.ID == db.self {
				continue seek
			}
			if now.Sub(db.lastPong(n.ID)) > maxAge {
				continue seek
			}
			for i := range nodes {
				if nodes[i].ID == n.ID {
					continue seek
				}
			}
			nodes = append(nodes,n)
		}
		return nodes
}

func (db *nodeDB) fetchTopicRegTickets(id NodeID) (issued, used uint32) {
	key := makeKey(id, nodeDBTopicRegTickets)
	blob, _ := db.lvl.Get(key,nil)
	if len(blob) != 8 {
		return 0,0
	}
	issued = binary.BigEndian.Uint32(blob[0:4])
	used = binary.BigEndian.Uint32(blob[4:8])
	return
}

func (db *nodeDB) updateTopicRegTickets(id NodeID, issued , used uint32) error {
	key := makeKey(id,nodeDBTopicRegTickets)
	blob := make([]byte,8)
	binary.BigEndian.PutUint32(blob[0:4],issued)
	binary.BigEndian.PutUint32(blob[4:8],used)
	return db.lvl.Put(key,blob,nil)
}

func nextNode(it iterator.Iterator) *Node {
	for end := false; !end; end = !it.Next() {
		id, field := splitKey(it.Key())
		if field != nodeDBDiscoverRoot {
			continue
		}
		var n Node
		if err := rlp.DecodeBytes(it.Value(), &n); err != nil {
			log.Warn(fmt.Sprintf("invalid node %x: %v",id, err))
			continue
		}
		return &n
	}
	return nil
}

func (db *nodeDB) close() {
	close(db.quit)
	db.lvl.Close()
}