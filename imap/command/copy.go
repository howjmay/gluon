package command

import (
	"fmt"
	"github.com/ProtonMail/gluon/imap/parser"
)

type CopyCommand struct {
	SeqSet  []SeqRange
	Mailbox string
}

func (l CopyCommand) String() string {
	return fmt.Sprintf("COPY %v '%v'", l.SeqSet, l.Mailbox)
}

func (l CopyCommand) SanitizedString() string {
	return fmt.Sprintf("COPY %v '%v'", l.SeqSet, sanitizeString(l.Mailbox))
}

type CopyCommandParser struct{}

func (CopyCommandParser) FromParser(p *parser.Parser) (Payload, error) {
	// copy            = "COPY" SP sequence-set SP mailbox
	if err := p.Consume(parser.TokenTypeSP, "expected space after command"); err != nil {
		return nil, err
	}

	seqSet, err := ParseSeqSet(p)
	if err != nil {
		return nil, err
	}

	if err := p.Consume(parser.TokenTypeSP, "expected space after seqset"); err != nil {
		return nil, err
	}

	mailbox, err := p.ParseMailbox()
	if err != nil {
		return nil, err
	}

	return &CopyCommand{
		SeqSet:  seqSet,
		Mailbox: mailbox,
	}, nil
}