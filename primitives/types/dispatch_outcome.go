package types

import (
	"bytes"
	"reflect"

	sc "github.com/LimeChain/goscale"
)

type DispatchOutcome sc.VaryingData

func NewDispatchOutcome(value sc.Encodable) DispatchOutcome {
	// None 			   = 0 - Extrinsic is valid and was submitted successfully.
	// DispatchError = 1 - Possible errors while dispatching the extrinsic.
	switch value.(type) {
	case DispatchError:
		return DispatchOutcome(sc.NewVaryingData(value))
	case sc.Empty, nil:
		return DispatchOutcome(sc.NewVaryingData(sc.Empty{}))
	default:
		panic("invalid DispatchOutcome option")
	}
}

func (o DispatchOutcome) Encode(buffer *bytes.Buffer) {
	value := o[0]

	switch reflect.TypeOf(value) {
	case reflect.TypeOf(*new(sc.Empty)):
		sc.U8(0).Encode(buffer)
	case reflect.TypeOf(*new(DispatchError)):
		sc.U8(1).Encode(buffer)
		value.Encode(buffer)
	default:
		panic("invalid DispatchOutcome type")
	}
}

func DecodeDispatchOutcome(buffer *bytes.Buffer) DispatchOutcome {
	b := sc.DecodeU8(buffer)

	switch b {
	case 0:
		return NewDispatchOutcome(sc.Empty{})
	case 1:
		value := DecodeDispatchError(buffer)
		return NewDispatchOutcome(value)
	default:
		panic("invalid DispatchOutcome type")
	}
}

func (o DispatchOutcome) Bytes() []byte {
	return sc.EncodedBytes(o)
}
