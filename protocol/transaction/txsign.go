package transaction

import (
	"srcd/crypto/ed25519/chainkd"
	"fmt"
)

//
func TxSign(tpl *Template,xprv chainkd.XPrv,xpub chainkd.XPub) error{
	//for i,sigInst := range tpl.SigningInstructions {
	//	h := tpl.Hash(uint32(i)).Byte32()
	//	sig := xprv.Sign(h[:])
	//	fmt.Printf("sig:%x\n",sig)
	//	rawTxSig := &RawTxSigWitness{
	//		Quorum: 1,
	//		Sigs:   []HexBytes{sig},
	//	}
	//	fmt.Println("111111111")
	//	fmt.Println(rawTxSig)
	//	sigInst.WitnessComponents = []witnessComponent{
	//		rawTxSig,
	//		sigInst.WitnessComponents...
	//	}
	//}
	h := tpl.Hash(0).Byte32()
	sig := xprv.Sign(h[:])
	pub := xpub.PublicKey()
	// Test with more signatures than required, in correct order
	tpl.SigningInstructions = []*SigningInstruction{{
		WitnessComponents: []witnessComponent{
			&RawTxSigWitness{
				Quorum: 1,
				Sigs:   []HexBytes{sig},
			},
			DataWitness([]byte(pub)),
		},
	}}
	//return nil
	return materializeWitnesses(tpl)
}

func materializeWitnesses(txTemplate *Template) error {
	msg := txTemplate.Transaction

	for i, sigInst := range txTemplate.SigningInstructions {
		var witness [][]byte
		for j, wc := range sigInst.WitnessComponents {
			err := wc.materialize(&witness)
			if err != nil {
				fmt.Printf("error in witness component %d of input %d", j, i)
			}
		}
		msg.SetInputArguments(sigInst.Position, witness)
	}

	return nil
}