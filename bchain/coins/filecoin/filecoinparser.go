package filecoin

import (
	faddr "github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/chain/types"
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
	return bchain.ChainEthereumType
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

func (f *FilecoinParser) filMessageToTx(msg *types.Message, blocktime int64, confirmations uint32) (*bchain.Tx, error) {
	vs, _ := new(big.Int).SetString(msg.Value.String(), 10)
	return &bchain.Tx{
		Blocktime:     blocktime,
		Confirmations: confirmations,
		Time:          blocktime,
		Txid:          msg.Cid().String(),
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
	addr, err := faddr.NewFromString(address)
	if err != nil {
		return nil, err
	}
	return addr.Marshal()
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
	var addr faddr.Address
	if err := addr.Unmarshal(addrDesc); err != nil {
		return nil, false, err
	}
	return []string{addr.String()}, true, nil
}

// GetScriptFromAddrDesc returns output script for given address descriptor
func (f *FilecoinParser) GetScriptFromAddrDesc(addrDesc bchain.AddressDescriptor) ([]byte, error) {
	return addrDesc, nil
}
