package filecoin

import (
	"context"
	"encoding/json"
	"github.com/filecoin-project/lotus/api/apistruct"
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

	Parser      *FilecoinParser
	Testnet     bool
	Network     string
	ChainConfig *Configuration
	Mempool     *bchain.MempoolFilecoinType

	close func()
}

// NewFilecoinRPC returns new FilecoinRPC instance.
func NewFilecoinRPC(config json.RawMessage, pushHandler func(bchain.NotificationType)) (bchain.BlockChain, error) {
	var cfg Configuration
	err := json.Unmarshal(config, &cfg)
	if err != nil {
		return nil, err
	}

	return &FilecoinRPC{
		ChainConfig: &cfg,
		Parser:      NewFilecoinParser(&cfg),
		BaseChain:   &bchain.BaseChain{},
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
func (f *FilecoinRPC) InitializeMempool(bchain.AddrDescForOutpointFunc, bchain.OnNewTxAddrFunc) error {
	// TODO
	return nil
}

// shutdown mempool, ZeroMQ and block chain connections
func (f *FilecoinRPC) Shutdown(ctx context.Context) error {
	f.close()
	return nil
}

func (f *FilecoinRPC) GetSubversion() string {
	// TODO
	return ""
}

func (f *FilecoinRPC) GetCoinName() string {
	// TODO
	return ""
}

func (f *FilecoinRPC) GetChainInfo() (*bchain.ChainInfo, error) {
	// TODO
	return nil, nil
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
	return "", nil
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
	ret := &bchain.BlockHeader{
		Height: uint32(height),
		Time:   int64(header.Timestamp),
		Hash:   header.Cid().String(),
	}
	// TODO: there are other fields that need to be added to the header
	return ret, nil
}

func (f *FilecoinRPC) GetBlock(hash string, height uint32) (*bchain.Block, error) {
	// TODO
	return nil, nil
}

func (f *FilecoinRPC) GetBlockInfo(hash string) (*bchain.BlockInfo, error) {
	// TODO
	return nil, nil
}

func (f *FilecoinRPC) GetMempoolTransactions() ([]string, error) {
	// TODO
	return nil, nil
}

func (f *FilecoinRPC) GetTransaction(txid string) (*bchain.Tx, error) {
	// TODO
	return nil, nil
}

func (f *FilecoinRPC) GetTransactionForMempool(txid string) (*bchain.Tx, error) {
	// TODO
	return nil, nil
}

func (f *FilecoinRPC) GetTransactionSpecific(tx *bchain.Tx) (json.RawMessage, error) {
	// TODO
	return nil, nil
}

func (f *FilecoinRPC) EstimateSmartFee(blocks int, conservative bool) (big.Int, error) {
	// TODO
	return big.Int{}, nil
}

func (f *FilecoinRPC) EstimateFee(blocks int) (big.Int, error) {
	// TODO
	return big.Int{}, nil
}

func (f *FilecoinRPC) SendRawTransaction(tx string) (string, error) {
	// TODO
	return "", nil
}

func (f *FilecoinRPC) GetMempoolEntry(txid string) (*bchain.MempoolEntry, error) {
	// TODO
	return nil, nil
}
