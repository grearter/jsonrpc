package jsonrpc

import (
	"encoding/json"
	"io"
	"net"
)

type Codec struct {
	Conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
}

func NewCodec(conn net.Conn) *Codec {
	return &Codec{
		Conn:    conn,
		encoder: json.NewEncoder(conn.(io.Writer)),
		decoder: json.NewDecoder(conn.(io.Reader)),
	}

}

func (codec *Codec) Encode(input interface{}) error {
	return codec.encoder.Encode(input)
}

func (codec *Codec) Decode(output interface{}) error {
	return codec.decoder.Decode(output)
}
