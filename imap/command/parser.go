package command

import (
	"fmt"
	"github.com/ProtonMail/gluon/imap/parser"
	"strings"
)

type Builder interface {
	FromParser(p *parser.Parser) (Payload, error)
}

// Parser parses IMAP Commands.
type Parser struct {
	parser   *parser.Parser
	commands map[string]Builder
	lastTag  string
	lastCmd  string
}

func NewParser(s *parser.Scanner) *Parser {
	return NewParserWithLiteralContinuationCb(s, nil)
}

func NewParserWithLiteralContinuationCb(s *parser.Scanner, cb func() error) *Parser {
	return &Parser{
		parser: parser.NewParserWithLiteralContinuationCb(s, cb),
		commands: map[string]Builder{
			"list":        &ListCommandParser{},
			"append":      &AppendCommandParser{},
			"search":      &SearchCommandParser{},
			"fetch":       &FetchCommandParser{},
			"capability":  &CapabilityCommandParser{},
			"idle":        &IdleCommandParser{},
			"noop":        &NoopCommandParser{},
			"logout":      &LogoutCommandParser{},
			"check":       &CheckCommandParser{},
			"close":       &CloseCommandParser{},
			"expunge":     &ExpungeCommandParser{},
			"unselect":    &UnselectCommandParser{},
			"starttls":    &StartTLSCommandParser{},
			"status":      &StatusCommandParser{},
			"select":      &SelectCommandParser{},
			"examine":     &ExamineCommandParser{},
			"create":      &CreateCommandParser{},
			"delete":      &DeleteCommandParser{},
			"subscribe":   &SubscribeCommandParser{},
			"unsubscribe": &UnsubscribeCommandParser{},
			"rename":      &RenameCommandParser{},
			"lsub":        &LSubCommandParser{},
			"login":       &LoginCommandParser{},
			"store":       &StoreCommandParser{},
			"copy":        &CopyCommandParser{},
			"move":        &MoveCommandParser{},
			"uid":         NewUIDCommandParser(),
		},
	}
}

func (p *Parser) LastParsedTag() string {
	return p.lastTag
}

func (p *Parser) LastParsedCommand() string {
	return p.lastCmd
}

func (p *Parser) Parse() (Command, error) {
	result := Command{}

	p.lastTag = ""
	p.lastCmd = ""
	p.parser.ResetOffsetCounter()

	if err := p.parser.Advance(); err != nil {
		return result, err
	}

	tag, err := p.parseTag()
	if err != nil {
		return result, err
	}

	// Done command does not have a tag.
	if strings.ToLower(tag) == "done" {
		p.lastCmd = "done"

		return Command{
			Tag:     "",
			Payload: &DoneCommand{},
		}, nil
	}

	result.Tag = tag
	p.lastTag = tag

	if err := p.parser.Consume(parser.TokenTypeSP, "Expected space after tag"); err != nil {
		return result, err
	}

	payload, err := p.parseCommand()
	if err != nil {
		return result, err
	}

	result.Payload = payload

	if err := p.parser.ConsumeNewLine(); err != nil {
		return result, err
	}

	return result, nil
}

func (p *Parser) parseCommand() (Payload, error) {
	var commandBytes []byte

	for {
		if ok, err := p.parser.Matches(parser.TokenTypeChar); err != nil {
			return nil, err
		} else if ok {
			commandBytes = append(commandBytes, parser.ByteToLower(p.parser.PreviousToken().Value))
		} else {
			break
		}
	}

	p.lastCmd = string(commandBytes)

	builder, ok := p.commands[p.lastCmd]
	if !ok {
		return nil, fmt.Errorf("unknown command '%v'", p.lastCmd)
	}

	return builder.FromParser(p.parser)
}

func (p *Parser) parseTag() (string, error) {
	// tag             = 1*<any ASTRING-CHAR except "+">
	isTagChar := func(tt parser.TokenType) bool {
		return parser.IsAStringChar(tt) && tt != parser.TokenTypePlus
	}

	if err := p.parser.ConsumeWith(isTagChar, "Invalid tag char detected"); err != nil {
		return "", err
	}

	tag, err := p.parser.CollectBytesWhileMatchesWithPrevWith(isTagChar)
	if err != nil {
		return "", err
	}

	return string(tag), err
}