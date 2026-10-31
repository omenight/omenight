package proto

import (
	"encoding/json"
	"io"
)

// MsgReader reads newline-delimited JSON messages off a channel.
type MsgReader struct{ dec *json.Decoder }

func NewMsgReader(r io.Reader) *MsgReader {
	return &MsgReader{dec: json.NewDecoder(r)}
}

func (m *MsgReader) Next() (Msg, error) {
	var msg Msg
	err := m.dec.Decode(&msg)
	return msg, err
}

func WriteMsg(w io.Writer, msg Msg) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
