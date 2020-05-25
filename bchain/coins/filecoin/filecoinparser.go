package filecoin

import (
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/martinboehm/btcd/wire"
	"github.com/martinboehm/btcutil/chaincfg"
	"github.com/trezor/blockbook/bchain"
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
	vs, err := hexutil.DecodeBig(msg.Value.String())
	if err != nil {
		return nil, err
	}
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
