package filecoin

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"strconv"
)

// Configuration represents json config file
type Configuration struct {
	CoinName                    string `json:"coin_name"`
	CoinShortcut                string `json:"coin_shortcut"`
	RPCURL                      string `json:"rpc_url"`
	RPCTimeout                  int    `json:"rpc_timeout"`
	RPCAuthToken                string `json:"rpc_auth_token"`
	BlockAddressesToKeep        int    `json:"block_addresses_to_keep"`
	MempoolTxTimeoutHours       int    `json:"mempoolTxTimeoutHours"`
	QueryBackendOnMempoolResync bool   `json:"queryBackendOnMempoolResync"`
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

	return &FilecoinRPC{
		ChainConfig: &cfg,
		Parser:      parser,
		BaseChain: &bchain.BaseChain{
			Parser: parser,
		},
		pushHandler: pushHandler,
		shutdown:    make(chan struct{}),
	}, nil
}

// initialize the block chain connector
func (f *FilecoinRPC) Initialize() error {
	ma, err := multiaddr.NewMultiaddr("/ip4/167.71.92.113/tcp/1235")
	if err != nil {
		return err
	}

	fullNode, close, err := lotus.New(ma, f.ChainConfig.RPCAuthToken)
	if err != nil {
		return err
	}
	f.fullNode = fullNode
	f.close = close

	chainChan, err := fullNode.ChainNotify(context.Background())
	if err != nil {
		return err
	}

	// TODO: how do we subscribe to pending transactions from the full node?

	go func() {
		for {
			select {
			case <-chainChan:
				f.pushHandler(bchain.NotificationNewBlock)
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

	return nil
}

// shutdown mempool, ZeroMQ and block chain connections
func (f *FilecoinRPC) Shutdown(ctx context.Context) error {
	f.close()
	close(f.shutdown)
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
	return tipSet.Cids()[0].String(), nil
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
	// TODO: what do we use for tipset key here
	tipSet, err := f.fullNode.ChainGetTipSetByHeight(context.Background(), abi.ChainEpoch(height), types.TipSetKey{})
	if err != nil {
		return "", err
	}
	return tipSet.Cids()[0].String(), nil
}

func (f *FilecoinRPC) GetBlockHeader(hash string) (*bchain.BlockHeader, error) {
	h, err := cid.Decode(hash)
	if err != nil {
		return nil, err
	}
	header, err := f.fullNode.ChainGetBlock(context.Background(), h)
	if err != nil {
		return nil, err
	}
	height, err := strconv.Atoi(header.Height.String())
	if err != nil {
		return nil, err
	}
	// TODO: confirmations, size, next?
	ret := &bchain.BlockHeader{
		Height: uint32(height),
		Time:   int64(header.Timestamp),
		Hash:   header.Cid().String(),
		Prev:   header.Parents[0].String(),
	}
	return ret, nil
}

func (f *FilecoinRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	var blkMessages *api.BlockMessages
	if hash != "" {
		h, err := cid.Decode(hash)
		if err != nil {
			return nil, err
		}
		blkMessages, err = f.fullNode.ChainGetBlockMessages(context.Background(), h)
		if err != nil {
			return nil, err
		}
	} else {
		tipSet, err := f.fullNode.ChainGetTipSetByHeight(context.Background(), abi.ChainEpoch(height), types.TipSetKey{})
		if err != nil {
			return nil, err
		}
		blkMessages, err = f.fullNode.ChainGetBlockMessages(context.Background(), tipSet.Cids()[0])
		if err != nil {
			return nil, err
		}
	}
	// TODO: this seems like an inefficient way to get the block txs. Is there an
	// API call to get everything at once?
	txs := make([]bchain.Tx, len(blkMessages.Cids))
	for i, id := range blkMessages.Cids {
		tx, err := f.GetTransaction(id.String())
		if err != nil {
			return nil, err
		}
		txs[i] = *tx

		if f.mempoolInitialized {
			f.Mempool.RemoveTransactionFromMempool(tx.Txid)
		}
	}
	blk := &bchain.Block{
		Txs: txs,
	}
	return blk, nil
}

func (f *FilecoinRPC) GetBlockInfo(hash string) (*bchain.BlockInfo, error) {
	h, err := cid.Decode(hash)
	if err != nil {
		return nil, err
	}
	header, err := f.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}
	blkHeader, err := f.fullNode.ChainGetBlock(context.Background(), h)
	if err != nil {
		return nil, err
	}
	messages, err := f.fullNode.ChainGetBlockMessages(context.Background(), h)
	if err != nil {
		return nil, err
	}
	txids := make([]string, len(messages.Cids))
	for i, id := range messages.Cids {
		txids[i] = id.String()
	}
	// TODO: verison, difficult, nonce, bits?
	bi := &bchain.BlockInfo{
		MerkleRoot:  blkHeader.Messages.String(), // TODO: is this right?
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
	h, err := cid.Decode(txid)
	if err != nil {
		return nil, err
	}
	message, err := f.fullNode.ChainGetMessage(context.Background(), h)
	if err != nil {
		return nil, err
	}
	// TODO: Figure out how to get the blocktime and height
	return f.Parser.(*FilecoinParser).filMessageToTx(message, 0, 0)
}

func (f *FilecoinRPC) GetTransactionForMempool(txid string) (*bchain.Tx, error) {
	return f.GetTransaction(txid)
}

func (f *FilecoinRPC) GetTransactionSpecific(tx *bchain.Tx) (json.RawMessage, error) {
	csd, ok := tx.CoinSpecificData.(*types.Message)
	if !ok {
		ntx, err := f.GetTransaction(tx.Txid)
		if err != nil {
			return nil, err
		}
		csd, ok = ntx.CoinSpecificData.(*types.Message)
		if !ok {
			return nil, errors.New("cannot get CoinSpecificData")
		}
	}
	m, err := json.Marshal(&csd)
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
