package command

import (
	"bytes"
	"testing"

	"github.com/ProtonMail/gluon/rfcparser"
	"github.com/stretchr/testify/require"
)

func TestParser_LoginCommandQuoted(t *testing.T) {
	input := toIMAPLine(`tag LOGIN "foo" "bar"`)
	s := rfcparser.NewScanner(bytes.NewReader(input))
	p := NewParser(s)

	expected := Command{Tag: "tag", Payload: &Login{
		UserID:   "foo",
		Password: "bar",
	}}

	cmd, err := p.Parse()
	require.NoError(t, err)
	require.Equal(t, expected, cmd)
	require.Equal(t, "login", p.LastParsedCommand())
	require.Equal(t, "tag", p.LastParsedTag())
}
