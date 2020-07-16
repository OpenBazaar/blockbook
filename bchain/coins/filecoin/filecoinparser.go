package filecoin

import (
	"encoding/hex"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/gogo/protobuf/proto"
	"github.com/ipfs/go-cid"
	"github.com/juju/errors"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
	"math/big"
)

const (
	MainnetMagic wire.BitcoinNet = 0xf1cfa6d3
	TestnetMagic wire.BitcoinNet = 0x0d221506

	// FilecoinAmountDecimalPoint defines number of decimal points in Ether amounts
	FilecoinAmountDecimalPoint = 18
)

var (
	MainNetParams chaincfg.Params
	TestNetParams chaincfg.Params
)

func init() {
	MainNetParams = chaincfg.MainNetParams
	MainNetParams.Net = MainnetMagic
	MainNetParams.PubKeyHashAddrID = []byte{58}
	MainNetParams.ScriptHashAddrID = []byte{50}
	MainNetParams.Bech32HRPSegwit = "qc"

	TestNetParams = chaincfg.TestNet3Params
	TestNetParams.Net = TestnetMagic
	TestNetParams.PubKeyHashAddrID = []byte{120}
	TestNetParams.ScriptHashAddrID = []byte{110}
	TestNetParams.Bech32HRPSegwit = "tq"
}

// FilecoinParser handle
type FilecoinParser struct {
	//*btc.BitcoinParser
	*bchain.BaseParser
	Config *Configuration
}

// NewFilecoinParser returns new DashParser instance
func NewFilecoinParser(c *Configuration) *FilecoinParser {
	return &FilecoinParser{
		BaseParser: &bchain.BaseParser{
			BlockAddressesToKeep: c.BlockAddressesToKeep,
			AmountDecimalPoint:   FilecoinAmountDecimalPoint,
		},
		Config: c,
	}
}

// GetChainType is type of the blockchain
func (f *FilecoinParser) GetChainType() bchain.ChainType {
	return bchain.ChainBitcoinType
}

// GetChainParams contains network parameters for the main Qtum network,
// the regression test Qtum network, the test Qtum network and
// the simulation test Qtum network, in this order
func GetChainParams(chain string) *chaincfg.Params {
	if !chaincfg.IsRegistered(&MainNetParams) {
		err := chaincfg.Register(&MainNetParams)
		if err == nil {
			err = chaincfg.Register(&TestNetParams)
		}
		if err != nil {
			panic(err)
		}
	}
	switch chain {
	case "test":
		return &TestNetParams
	default:
		return &MainNetParams
	}
}

func (f *FilecoinParser) filMessageToTx(msg *types.Message) (*bchain.Tx, error) {
	vs, _ := new(big.Int).SetString(msg.Value.String(), 10)
	return &bchain.Tx{
		Txid:    msg.Cid().String(),
		Version: int32(msg.Version),
		Vin: []bchain.Vin{
			{
				Addresses: []string{msg.From.String()},
			},
		},
		Vout: []bchain.Vout{
			{
				N:        0,
				ValueSat: *vs,
				ScriptPubKey: bchain.ScriptPubKey{
					Addresses: []string{msg.To.String()},
				},
			},
		},
		CoinSpecificData: msg,
	}, nil
}

// GetAddrDescFromAddress returns internal address representation of given address
func (f *FilecoinParser) GetAddrDescFromAddress(address string) (bchain.AddressDescriptor, error) {
	return []byte(address), nil
}

// GetAddrDescFromVout returns internal address representation of given transaction output
func (f *FilecoinParser) GetAddrDescFromVout(output *bchain.Vout) (bchain.AddressDescriptor, error) {
	if len(output.ScriptPubKey.Addresses) != 1 {
		return nil, bchain.ErrAddressMissing
	}
	return f.GetAddrDescFromAddress(output.ScriptPubKey.Addresses[0])
}

// GetAddressesFromAddrDesc returns addresses for given address descriptor with flag if the addresses are searchable
func (f *FilecoinParser) GetAddressesFromAddrDesc(addrDesc bchain.AddressDescriptor) ([]string, bool, error) {
	return []string{string(addrDesc)}, true, nil
}

// GetScriptFromAddrDesc returns output script for given address descriptor
func (f *FilecoinParser) GetScriptFromAddrDesc(addrDesc bchain.AddressDescriptor) ([]byte, error) {
	return addrDesc, nil
}

// PackedTxidLen returns length in bytes of packed txid
func (f *FilecoinParser) PackedTxidLen() int {
	return 38
}

// PackTx packs transaction to byte array using protobuf
func (f *FilecoinParser) PackTx(tx *bchain.Tx, height uint32, blockTime int64) ([]byte, error) {
	var err error
	pti := make([]*bchain.ProtoTransaction_VinType, len(tx.Vin))
	for i, vi := range tx.Vin {
		hex, err := hex.DecodeString(vi.ScriptSig.Hex)
		if err != nil {
			return nil, errors.Annotatef(err, "Vin %v Hex %v", i, vi.ScriptSig.Hex)
		}
		// coinbase txs do not have Vin.txid
		itxid, err := f.PackTxid(vi.Txid)
		if err != nil && err != bchain.ErrTxidMissing {
			return nil, errors.Annotatef(err, "Vin %v Txid %v", i, vi.Txid)
		}
		pti[i] = &bchain.ProtoTransaction_VinType{
			Addresses:    vi.Addresses,
			Coinbase:     vi.Coinbase,
			ScriptSigHex: hex,
			Sequence:     vi.Sequence,
			Txid:         itxid,
			Vout:         vi.Vout,
		}
	}
	pto := make([]*bchain.ProtoTransaction_VoutType, len(tx.Vout))
	for i, vo := range tx.Vout {
		hex, err := hex.DecodeString(vo.ScriptPubKey.Hex)
		if err != nil {
			return nil, errors.Annotatef(err, "Vout %v Hex %v", i, vo.ScriptPubKey.Hex)
		}
		pto[i] = &bchain.ProtoTransaction_VoutType{
			Addresses:       vo.ScriptPubKey.Addresses,
			N:               vo.N,
			ScriptPubKeyHex: hex,
			ValueSat:        vo.ValueSat.Bytes(),
		}
	}
	pt := &bchain.ProtoTransaction{
		Blocktime: uint64(blockTime),
		Height:    height,
		Locktime:  tx.LockTime,
		Vin:       pti,
		Vout:      pto,
		Version:   tx.Version,
	}
	if pt.Hex, err = hex.DecodeString(tx.Hex); err != nil {
		return nil, errors.Annotatef(err, "Hex %v", tx.Hex)
	}
	if pt.Txid, err = f.PackTxid(tx.Txid); err != nil {
		return nil, errors.Annotatef(err, "Txid %v", tx.Txid)
	}
	return proto.Marshal(pt)
}

// UnpackTx unpacks transaction from protobuf byte array
func (f *FilecoinParser) UnpackTx(buf []byte) (*bchain.Tx, uint32, error) {
	var pt bchain.ProtoTransaction
	err := proto.Unmarshal(buf, &pt)
	if err != nil {
		return nil, 0, err
	}
	txid, err := f.UnpackTxid(pt.Txid)
	if err != nil {
		return nil, 0, err
	}
	vin := make([]bchain.Vin, len(pt.Vin))
	for i, pti := range pt.Vin {
		itxid, err := f.UnpackTxid(pti.Txid)
		if err != nil {
			return nil, 0, err
		}
		vin[i] = bchain.Vin{
			Addresses: pti.Addresses,
			Coinbase:  pti.Coinbase,
			ScriptSig: bchain.ScriptSig{
				Hex: hex.EncodeToString(pti.ScriptSigHex),
			},
			Sequence: pti.Sequence,
			Txid:     itxid,
			Vout:     pti.Vout,
		}
	}
	vout := make([]bchain.Vout, len(pt.Vout))
	for i, pto := range pt.Vout {
		var vs big.Int
		vs.SetBytes(pto.ValueSat)
		vout[i] = bchain.Vout{
			N: pto.N,
			ScriptPubKey: bchain.ScriptPubKey{
				Addresses: pto.Addresses,
				Hex:       hex.EncodeToString(pto.ScriptPubKeyHex),
			},
			ValueSat: vs,
		}
	}
	tx := bchain.Tx{
		Blocktime: int64(pt.Blocktime),
		Hex:       hex.EncodeToString(pt.Hex),
		LockTime:  pt.Locktime,
		Time:      int64(pt.Blocktime),
		Txid:      txid,
		Vin:       vin,
		Vout:      vout,
		Version:   pt.Version,
	}
	return &tx, pt.Height, nil
}

// PackTxid packs txid to byte array
func (f *FilecoinParser) PackTxid(txid string) ([]byte, error) {
	if txid == "" {
		return nil, bchain.ErrTxidMissing
	}
	id, err := cid.Decode(txid)
	if err != nil {
		return nil, err
	}
	return id.Bytes(), nil
}

// UnpackTxid unpacks byte array to txid
func (f *FilecoinParser) UnpackTxid(buf []byte) (string, error) {
	_, id, err := cid.CidFromBytes(buf)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
