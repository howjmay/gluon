package command

import (
	"fmt"
	"github.com/ProtonMail/gluon/imap/parser"
)

type UIDCommand struct {
	Command Payload
}

func (l UIDCommand) String() string {
	return fmt.Sprintf("UID %v", l.Command.String())
}

func (l UIDCommand) SanitizedString() string {
	return fmt.Sprintf("UID %v", l.Command.SanitizedString())
}

type UIDCommandParser struct {
	commands map[string]Builder
}

func NewUIDCommandParser() *UIDCommandParser {
	return &UIDCommandParser{
		commands: map[string]Builder{
			"copy":   &CopyCommandParser{},
			"fetch":  &FetchCommandParser{},
			"search": &SearchCommandParser{},
			"move":   &MoveCommandParser{},
			"store":  &StoreCommandParser{},
		}}
}

func (u *UIDCommandParser) FromParser(p *parser.Parser) (Payload, error) {
	// uid             = "UID" SP (copy / fetch / search / store)
	// uidExpunge      = "UID" SP "EXPUNGE"
	if err := p.Consume(parser.TokenTypeSP, "expected space after command"); err != nil {
		return nil, err
	}

	var commandBytes []byte

	for {
		if ok, err := p.Matches(parser.TokenTypeChar); err != nil {
			return nil, err
		} else if ok {
			commandBytes = append(commandBytes, parser.ByteToLower(p.PreviousToken().Value))
		} else {
			break
		}
	}

	command := string(commandBytes)

	// Special case to handle uid expunge extension
	if command == "expunge" {
		return UIDExpungeCommandParser{}.FromParser(p)
	}

	builder, ok := u.commands[command]
	if !ok {
		return nil, fmt.Errorf("unknown uid command '%v'", command)
	}

	payload, err := builder.FromParser(p)
	if err != nil {
		return nil, err
	}

	return &UIDCommand{
		Command: payload,
	}, nil
}

type UIDExpungeCommand struct {
	SeqSet []SeqRange
}

func (l UIDExpungeCommand) String() string {
	return fmt.Sprintf("UID EXPUNGE %v", l.SeqSet)
}

func (l UIDExpungeCommand) SanitizedString() string {
	return l.String()
}

type UIDExpungeCommandParser struct{}

func (UIDExpungeCommandParser) FromParser(p *parser.Parser) (Payload, error) {
	if err := p.Consume(parser.TokenTypeSP, "expected space after command"); err != nil {
		return nil, err
	}

	seqSet, err := ParseSeqSet(p)
	if err != nil {
		return nil, err
	}

	return &UIDExpungeCommand{SeqSet: seqSet}, nil
}