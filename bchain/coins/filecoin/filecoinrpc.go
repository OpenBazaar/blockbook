package filecoin

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cenkalti/backoff/v3"
	"github.com/dgraph-io/badger/v2"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/apistruct"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/specs-actors/actors/abi"
	"github.com/golang/glog"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multiaddr"
	"github.com/textileio/powergate/lotus"
	"github.com/trezor/blockbook/bchain"
	"math/big"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	dbHeadKey = "tipsetDBHead"
)

// Configuration represents json config file
type Configuration struct {
	CoinName                    string `json:"coin_name"`
	CoinShortcut                string `json:"coin_shortcut"`
	RPCIP                       string `json:"rpc_ip"`
	RPCTimeout                  int    `json:"rpc_timeout"`
	RPCAuthToken                string `json:"rpc_auth_token"`
	BlockAddressesToKeep        int    `json:"block_addresses_to_keep"`
	MempoolTxTimeoutHours       int    `json:"mempoolTxTimeoutHours"`
	QueryBackendOnMempoolResync bool   `json:"queryBackendOnMempoolResync"`
	DataPath                    string `json:"data_path"`
}

// FilecoinRPC is an interface to JSON-RPC lotus filecoin service.
type FilecoinRPC struct {
	*bchain.BaseChain
	fullNode *apistruct.FullNodeStruct

	Parser             bchain.BlockChainParser
	Testnet            bool
	Network            string
	ChainConfig        *Configuration
	Mempool            *bchain.MempoolFilecoinType
	mempoolInitialized bool
	pushHandler        func(notificationType bchain.NotificationType)

	db    *badger.DB
	dbMtx sync.Mutex

	shutdown chan struct{}
	close    func()
}

// NewFilecoinRPC returns new FilecoinRPC instance.
func NewFilecoinRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	var cfg Configuration
	err := json.Unmarshal(config, &cfg)
	if err != nil {
		return nil, err
	}
	parser := NewFilecoinParser(&cfg)
	p := path.Join(cfg.DataPath, "filecoin")
	os.MkdirAll(p, os.ModePerm)

	db, err := badger.Open(badger.DefaultOptions(p))
	if err != nil {
		return nil, err
	}

	return &FilecoinRPC{
		ChainConfig: &cfg,
		Parser:      parser,
		BaseChain: &bchain.BaseChain{
			Parser: parser,
		},
		db:          db,
		dbMtx:       sync.Mutex{},
		pushHandler: pushHandler,
		shutdown:    make(chan struct{}),
	}, nil
}

// initialize the block chain connector
func (f *FilecoinRPC) Initialize() error {
	s := strings.Split(f.ChainConfig.RPCIP, ":")
	if len(s) != 2 {
		return errors.New("invalid filecoin server IP")
	}
	ma, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%s", s[0], s[1]))
	if err != nil {
		return err
	}

	fullNode, close, err := lotus.New(ma, f.ChainConfig.RPCAuthToken, 3)
	if err != nil {
		return err
	}
	f.fullNode = fullNode
	f.close = close

	chainChan := f.subscribeChain()

	var startHeight, height, headHeight uint64
	f.dbMtx.Lock()
	err = f.db.View(func(tx *badger.Txn) error {
		item, err := tx.Get([]byte(dbHeadKey))
		if err != nil && err != badger.ErrKeyNotFound {
			return err
		}
		if item != nil {
			headBytes, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			headHeight = binary.BigEndian.Uint64(headBytes)
		}
		return nil
	})
	f.dbMtx.Unlock()
	if err != nil {
		return err
	}

	tipSet, err := f.fullNode.ChainHead(context.Background())
	if err != nil {
		return err
	}
	height, startHeight = uint64(tipSet.Height()), uint64(tipSet.Height())

	f.dbMtx.Lock()
	err = f.db.Update(func(tx *badger.Txn) error {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, height)
		if err := tx.Set(hashTipsetKey(tipSet.Key()), tipSet.Key().Bytes()); err != nil {
			return err
		}
		return tx.Set(key, tipSet.Key().Bytes())
	})
	f.dbMtx.Unlock()
	if err != nil {
		return err
	}

	for height > headHeight {
		tipSet, err = f.fullNode.ChainGetTipSet(context.Background(), tipSet.Parents())
		if err != nil {
			return err
		}
		f.dbMtx.Lock()
		err := f.db.Update(func(tx *badger.Txn) error {
			if uint64(tipSet.Height()) < height-1 {
				h := height - 1
				for h > uint64(tipSet.Height()) {
					key := make([]byte, 8)
					binary.BigEndian.PutUint64(key, h)
					nilTs := createNilTipsetKey(h)
					if err := tx.Set(key, nilTs); err != nil {
						return err
					}
					if err := tx.Set(nilTs, key); err != nil {
						return err
					}
					h--
				}
			}
			height = uint64(tipSet.Height())
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, height)
			if err := tx.Set(hashTipsetKey(tipSet.Key()), tipSet.Key().Bytes()); err != nil {
				return err
			}
			return tx.Set(key, tipSet.Key().Bytes())
		})
		f.dbMtx.Unlock()
		if err != nil {
			return err
		}
		fmt.Println("Cached filecoin tipset at height", uint32(tipSet.Height()))
	}

	f.dbMtx.Lock()
	err = f.db.Update(func(tx *badger.Txn) error {
		height := make([]byte, 8)
		binary.BigEndian.PutUint64(height, startHeight)
		return tx.Set([]byte(dbHeadKey), height)
	})
	f.dbMtx.Unlock()
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case _, ok := <-chainChan:
				f.pushHandler(bchain.NotificationNewBlock)
				if !ok {
					chainChan = f.subscribeChain()
				}
			case <-f.shutdown:
				return
			}
		}
	}()

	return nil
}

// create mempool but do not initialize it
func (f *FilecoinRPC) CreateMempool(chain bchain.BlockChain) (bchain.Mempool, error) {
	if f.Mempool == nil {
		f.Mempool = bchain.NewMempoolFilecoinType(chain, f.ChainConfig.MempoolTxTimeoutHours, f.ChainConfig.QueryBackendOnMempoolResync)
		glog.Info("mempool created, MempoolTxTimeoutHours=", f.ChainConfig.MempoolTxTimeoutHours, ", QueryBackendOnMempoolResync=", f.ChainConfig.QueryBackendOnMempoolResync)
	}
	return f.Mempool, nil
}

// initialize mempool, create ZeroMQ (or other) subscription
func (f *FilecoinRPC) InitializeMempool(addrDescForOutpoint bchain.AddrDescForOutpointFunc, onNewTxAddr bchain.OnNewTxAddrFunc) error {
	if f.Mempool == nil {
		return errors.New("mempool not created")
	}

	// get initial mempool transactions
	txs, err := f.GetMempoolTransactions()
	if err != nil {
		return err
	}
	for _, txid := range txs {
		f.Mempool.AddTransactionToMempool(txid)
	}

	f.Mempool.OnNewTxAddr = onNewTxAddr

	f.mempoolInitialized = true

	mempoolChan := f.subscribeMempool()

	go func() {
		for {
			select {
			case tx, closed := <-mempoolChan:
				if tx.Message != nil {
					f.Mempool.AddTransactionToMempool(tx.Message.Cid().String())
					f.pushHandler(bchain.NotificationNewTx)
				}
				if closed {
					mempoolChan = f.subscribeMempool()
				}
			case <-f.shutdown:
				return
			}
		}
	}()

	return nil
}

func (f *FilecoinRPC) subscribeMempool() <-chan api.MpoolUpdate {
	bo := backoff.NewExponentialBackOff()
	for {
		mempoolChan, err := f.fullNode.MpoolSub(context.Background())
		if err != nil {
			glog.Errorf("Error connecting to filecoin mempool subscription: %s", err)
		} else {
			return mempoolChan
		}

		select {
		case <-time.After(bo.NextBackOff()):
			continue
		case <-f.shutdown:
			return nil
		}
	}
}

func (f *FilecoinRPC) subscribeChain() <-chan []*api.HeadChange {
	bo := backoff.NewExponentialBackOff()
	for {
		chainChan, err := f.fullNode.ChainNotify(context.Background())
		if err != nil {
			glog.Errorf("Error connecting to filecoin chain subscription: %s", err)
		} else {
			return chainChan
		}

		select {
		case <-time.After(bo.NextBackOff()):
			continue
		case <-f.shutdown:
			return nil
		}
	}
}

// shutdown mempool, ZeroMQ and block chain connections
func (f *FilecoinRPC) Shutdown(ctx context.Context) error {
	f.close()
	close(f.shutdown)
	f.db.Close()
	return nil
}

func (f *FilecoinRPC) GetSubversion() string {
	ver, _ := f.fullNode.Version(context.Background())
	return ver.String()
}

func (f *FilecoinRPC) GetCoinName() string {
	return f.ChainConfig.CoinName
}

func (f *FilecoinRPC) GetChainInfo() (*bchain.ChainInfo, error) {
	hash, err := f.GetBestBlockHash()
	if err != nil {
		return nil, err
	}
	height, err := f.GetBestBlockHeight()
	if err != nil {
		return nil, err
	}
	ver, _ := f.fullNode.Version(context.Background())

	// TODO: difficuty, protoco version, size on disk, timeoffset, warnings?
	return &bchain.ChainInfo{
		Blocks:        int(height),
		Bestblockhash: hash,
		Headers:       int(height),
		Subversion:    f.GetSubversion(),
		Version:       ver.APIVersion.String(),
		Chain:         f.ChainConfig.CoinName,
	}, nil
}

func (f *FilecoinRPC) GetBestBlockHash() (string, error) {
	tipSet, err := f.fullNode.ChainHead(context.Background())
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(hashTipsetKey(tipSet.Key())), nil
}

func (f *FilecoinRPC) GetBestBlockHeight() (uint32, error) {
	tipSet, err := f.fullNode.ChainHead(context.Background())
	if err != nil {
		return 0, err
	}
	height, err := strconv.Atoi(tipSet.Height().String())
	if err != nil {
		return 0, err
	}
	return uint32(height), nil
}

func (f *FilecoinRPC) GetBlockHash(height uint32) (string, error) {
	var blockHash string
	f.dbMtx.Lock()
	err := f.db.View(func(tx *badger.Txn) error {
		heightBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(heightBytes, uint64(height))
		data, err := tx.Get(heightBytes)
		if err != nil {
			return fmt.Errorf("error fetching tipset from db at height %d: %s", height, err)
		}
		tsBytes, err := data.ValueCopy(nil)
		if err != nil {
			return err
		}
		if isNilTipsetKey(tsBytes) {
			blockHash = hex.EncodeToString(tsBytes)
		} else {
			tsk, err := types.TipSetKeyFromBytes(tsBytes)
			if err != nil {
				return err
			}
			blockHash = hex.EncodeToString(hashTipsetKey(tsk))
		}
		return nil
	})
	f.dbMtx.Unlock()
	if err != nil {
		glog.Infof("Fetching block hash for height %d", height)
		tipSet, err := f.fullNode.ChainHead(context.Background())
		if err != nil {
			return "", nil
		}
		// This call is very expensive. Only do for less than 6000 from tip.
		if int32(tipSet.Height())-int32(height) < 6000 {
			tipSet, err := f.fullNode.ChainGetTipSetByHeight(context.Background(), abi.ChainEpoch(height), types.EmptyTSK)
			if err != nil {
				return "", nil
			}
			f.dbMtx.Lock()
			err = f.db.Update(func(tx *badger.Txn) error {
				key := make([]byte, 8)
				binary.BigEndian.PutUint64(key, uint64(height))
				if err := tx.Set(hashTipsetKey(tipSet.Key()), tipSet.Key().Bytes()); err != nil {
					return err
				}
				return tx.Set(key, tipSet.Key().Bytes())
			})
			f.dbMtx.Unlock()
			if err != nil {
				return "", err
			}
			return hex.EncodeToString(hashTipsetKey(tipSet.Key())), nil
		}
		return "", fmt.Errorf("tipset not found for height %d", height)
	}
	return blockHash, nil
}

func (f *FilecoinRPC) GetBlockHeader(hash string) (*bchain.BlockHeader, error) {
	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		return nil, err
	}

	var (
		height    uint64
		parents   string
		timestamp uint64
		size      int
	)
	if isNilTipsetKey(hashBytes) {
		f.dbMtx.Lock()
		err := f.db.View(func(tx *badger.Txn) error {
			data, err := tx.Get(hashBytes)
			if err != nil {
				return fmt.Errorf("error fetching tipset from db %s: %s", hex.EncodeToString(hashBytes), err)
			}
			heightBytes, err := data.ValueCopy(nil)
			if err != nil {
				return err
			}
			height = binary.BigEndian.Uint64(heightBytes)
			return nil
		})
		f.dbMtx.Unlock()
		if err != nil {
			return nil, err
		}
		parents, err = f.GetBlockHash(uint32(height) - 1)
		if err != nil {
			glog.Warningf("GetBlockHeader: Failed to load prev block hash %s", err)
		}
	} else {
		var tipSetKey types.TipSetKey
		f.dbMtx.Lock()
		err := f.db.View(func(tx *badger.Txn) error {
			data, err := tx.Get(hashBytes)
			if err != nil {
				return fmt.Errorf("error fetching tipset key from db %s: %s", hash, err)
			}
			tipSetBytes, err := data.ValueCopy(nil)
			if err != nil {
				return err
			}
			tipSetKey, err = types.TipSetKeyFromBytes(tipSetBytes)
			if err != nil {
				return err
			}
			return nil
		})
		f.dbMtx.Unlock()
		if err != nil {
			return nil, err
		}
		ts, err := f.fullNode.ChainGetTipSet(context.Background(), tipSetKey)
		if err != nil {
			return nil, err
		}
		height = uint64(ts.Height())
		parents = hex.EncodeToString(hashTipsetKey(ts.Parents()))
		timestamp = ts.Blocks()[0].Timestamp
		size = len(ts.Cids())
	}

	bestHeight, err := f.GetBestBlockHeight()
	if err != nil {
		return nil, err
	}

	var nextHash string
	if uint64(bestHeight) != height {
		nextHash, err = f.GetBlockHash(uint32(height) + 1)
		if err != nil {
			glog.Warningf("GetBlockHeader: Failed to load next block hash %s", err)
		}
	}
	conf := 0
	if bestHeight > 0 {
		conf = int(bestHeight - uint32(height) + 1)
	}
	ret := &bchain.BlockHeader{
		Height:        uint32(height),
		Time:          int64(timestamp),
		Hash:          hash,
		Prev:          parents,
		Confirmations: conf,
		Next:          nextHash,
		Size:          size,
	}
	return ret, nil
}

func (f *FilecoinRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	var (
		tipSetBytes []byte
		header      *bchain.BlockHeader
		err         error
	)
	if hash != "" {
		tipSetBytes, err = hex.DecodeString(hash)
		if err != nil {
			return nil, err
		}
		header, err = f.GetBlockHeader(hash)
		if err != nil {
			return nil, bchain.ErrBlockNotFound
		}
		height = header.Height
	} else {
		hash, err = f.GetBlockHash(height)
		if err != nil {
			glog.Warningf("Error querying for bock hash at height %d", height)
		}
	}
	f.dbMtx.Lock()
	err = f.db.View(func(tx *badger.Txn) error {
		heightBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(heightBytes, uint64(height))
		data, err := tx.Get(heightBytes)
		if err != nil {
			return err
		}
		tipSetBytes, err = data.ValueCopy(nil)
		if err != nil {
			return err
		}
		return nil
	})
	f.dbMtx.Unlock()
	if err != nil {
		return nil, bchain.ErrBlockNotFound
	}

	msgMap := make(map[cid.Cid]struct{})
	if !isNilTipsetKey(tipSetBytes) {
		tipSetKey, err := types.TipSetKeyFromBytes(tipSetBytes)
		if err != nil {
			return nil, err
		}
		hash = hex.EncodeToString(hashTipsetKey(tipSetKey))

		tipSet, err := f.fullNode.ChainGetTipSet(context.Background(), tipSetKey)
		if err != nil {
			return nil, err
		}

		for _, id := range tipSet.Cids() {
			blockMessages, err := f.fullNode.ChainGetBlockMessages(context.Background(), id)
			if err != nil {
				return nil, err
			}
			for _, c := range blockMessages.Cids {
				msgMap[c] = struct{}{}
			}
		}
	}

	if header == nil {
		header, err = f.GetBlockHeader(hash)
		if err != nil {
			return nil, err
		}
	}

	// TODO: this seems like an inefficient way to get the block txs. Is there an
	// API call to get everything at once?
	txs := make([]bchain.Tx, 0, len(msgMap))
	heightBytes := make([]byte, 8)
	blockTimeBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, uint64(height))
	binary.BigEndian.PutUint64(blockTimeBytes, uint64(header.Time))

	bestHeight, err := f.GetBestBlockHeight()
	if err != nil {
		return nil, err
	}

	for id := range msgMap {
		tx, err := f.getTransaction(id.String(), bestHeight)
		if err != nil {
			return nil, err
		}
		tx.BlockHeight = height
		tx.Blocktime = header.Time
		tx.Time = header.Time
		txs = append(txs, *tx)

		if f.mempoolInitialized {
			f.Mempool.RemoveTransactionFromMempool(tx.Txid)
		}
		f.dbMtx.Lock()
		err = f.db.Update(func(tx *badger.Txn) error {
			return tx.Set(id.Bytes(), append(heightBytes, blockTimeBytes...))
		})
		f.dbMtx.Unlock()
		if err != nil {
			return nil, bchain.ErrBlockNotFound
		}
	}
	blk := &bchain.Block{
		Txs:         txs,
		BlockHeader: *header,
	}
	return blk, nil
}

func (f *FilecoinRPC) GetBlockInfo(hash string) (*bchain.BlockInfo, error) {
	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		return nil, err
	}
	msgMap := make(map[cid.Cid]struct{})
	if !isNilTipsetKey(hashBytes) {
		var tipSetBytes []byte
		f.dbMtx.Lock()
		err = f.db.View(func(tx *badger.Txn) error {
			data, err := tx.Get(hashBytes)
			if err != nil {
				return err
			}
			tipSetBytes, err = data.ValueCopy(nil)
			if err != nil {
				return err
			}
			return nil
		})
		f.dbMtx.Unlock()
		if err != nil {
			return nil, bchain.ErrBlockNotFound
		}
		tipSetKey, err := types.TipSetKeyFromBytes(tipSetBytes)
		if err != nil {
			return nil, err
		}
		tipSet, err := f.fullNode.ChainGetTipSet(context.Background(), tipSetKey)
		if err != nil {
			return nil, err
		}

		for _, id := range tipSet.Cids() {
			blockMessages, err := f.fullNode.ChainGetBlockMessages(context.Background(), id)
			if err != nil {
				return nil, err
			}
			for _, c := range blockMessages.Cids {
				msgMap[c] = struct{}{}
			}
		}
	}

	header, err := f.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}

	txids := make([]string, 0, len(msgMap))
	for id := range msgMap {
		txids = append(txids, id.String())
	}
	// TODO: verison, difficult, nonce, bits?
	bi := &bchain.BlockInfo{
		Txids:       txids,
		BlockHeader: *header,
	}
	return bi, nil
}

func (f *FilecoinRPC) GetMempoolTransactions() ([]string, error) {
	// TODO: what do we use for tipsetkey here?
	msgs, err := f.fullNode.MpoolPending(context.Background(), types.TipSetKey{})
	if err != nil {
		return nil, err
	}
	txids := make([]string, len(msgs))
	for i, msg := range msgs {
		txids[i] = msg.Cid().String()
	}
	return txids, nil
}

func (f *FilecoinRPC) GetTransaction(txid string) (*bchain.Tx, error) {
	height, err := f.GetBestBlockHeight()
	if err != nil {
		return nil, err
	}
	return f.getTransaction(txid, height)
}

func (f *FilecoinRPC) getTransaction(txid string, chainHeight uint32) (*bchain.Tx, error) {
	h, err := cid.Decode(txid)
	if err != nil {
		return nil, err
	}
	message, err := f.fullNode.ChainGetMessage(context.Background(), h)
	if err != nil {
		return nil, bchain.ErrTxNotFound
	}
	f.dbMtx.Lock()
	var (
		height uint64
		blockTime uint64
	)
	err = f.db.View(func(dbtx *badger.Txn) error {
		data, err := dbtx.Get(h.Bytes())
		if err == nil {
			respBytes, err := data.ValueCopy(nil)
			if err == nil {
				height = binary.BigEndian.Uint64(respBytes[:8])
				blockTime = binary.BigEndian.Uint64(respBytes[8:])
			}
		}
		return nil
	})
	f.dbMtx.Unlock()
	tx, err := f.Parser.(*FilecoinParser).filMessageToTx(message, height)
	if err != nil {
		return nil, err
	}

	conf := uint32(0)
	if height > 0 {
		conf = chainHeight - uint32(height) + 1
	}
	tx.BlockHeight = uint32(height)
	tx.Confirmations = conf
	tx.Blocktime = int64(blockTime)
	tx.Time = time.Now().Unix()
	tx.Txid = txid

	ser, err := message.Serialize()
	if err != nil {
		return nil, err
	}
	tx.Hex = hex.EncodeToString(ser)
	return tx, nil
}

func (f *FilecoinRPC) GetTransactionForMempool(txid string) (*bchain.Tx, error) {
	return f.GetTransaction(txid)
}

func (f *FilecoinRPC) GetTransactionSpecific(tx *bchain.Tx) (json.RawMessage, error) {
	m, err := json.Marshal(tx.CoinSpecificData)
	return json.RawMessage(m), err
}

func (f *FilecoinRPC) EstimateSmartFee(blocks int, conservative bool) (big.Int, error) {
	// TODO: what do we use for address, limit, and tipsetkey here?
	fee, err := f.fullNode.MpoolEstimateGasPrice(context.Background(), uint64(blocks), address.Address{}, 0, types.TipSetKey{})
	if err != nil {
		return big.Int{}, err
	}
	return *fee.Int, nil
}

func (f *FilecoinRPC) EthereumTypeGetBalance(addrDesc bchain.AddressDescriptor) (*big.Int, error) {
	strAddress, err := address.NewFromString(string(addrDesc))
	if err != nil {
		return nil, err
	}

	balance, err := f.fullNode.WalletBalance(context.Background(), strAddress)
	if err != nil {
		return nil, err
	}

	return balance.Int, nil
}

func (f *FilecoinRPC) EstimateFee(blocks int) (big.Int, error) {
	return f.EstimateSmartFee(blocks, true)
}

func (f *FilecoinRPC) SendRawTransaction(tx string) (string, error) {
	b, err := hex.DecodeString(tx)
	if err != nil {
		return "", err
	}

	var msg types.SignedMessage
	if err := msg.UnmarshalCBOR(bytes.NewReader(b)); err != nil {
		return "", err
	}
	id, err := f.fullNode.MpoolPush(context.Background(), &msg)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// EthereumTypeGetNonce returns current balance of an address
func (f *FilecoinRPC) EthereumTypeGetNonce(addrDesc bchain.AddressDescriptor) (uint64, error) {
	return 0, nil
}

func (f *FilecoinRPC) EthereumTypeGetErc20ContractInfo(contractDesc bchain.AddressDescriptor) (*bchain.Erc20Contract, error) {
	return nil, nil
}

func hashTipsetKey(tsk types.TipSetKey) []byte {
	h := sha512.Sum512(tsk.Bytes())
	return h[:38]
}

func createNilTipsetKey(height uint64) []byte {
	h := make([]byte, 8)
	binary.BigEndian.PutUint64(h, height)

	z := make([]byte, 30)
	return append(z, h...)
}

func isNilTipsetKey(tsk []byte) bool {
	z := make([]byte, 30)
	return bytes.Equal(z, tsk[:30])
}
