package session

import (
	"context"

	context2 "github.com/ProtonMail/gluon/internal/contexts"
	"github.com/ProtonMail/gluon/internal/parser/proto"
	"github.com/ProtonMail/gluon/internal/response"
	"github.com/ProtonMail/gluon/internal/state"
)

func (s *Session) handleExpunge(ctx context.Context, tag string, cmd *proto.Expunge, mailbox *state.Mailbox, ch chan response.Response) (response.Response, error) {
	if mailbox.ReadOnly() {
		return nil, ErrReadOnly
	}

	if err := mailbox.Expunge(ctx, nil); err != nil {
		return nil, err
	}

	if err := flush(ctx, mailbox, true, ch); err != nil {
		return nil, err
	}

	return response.Ok(tag).WithMessage("EXPUNGE"), nil
}

func (s *Session) handleUIDExpunge(ctx context.Context, tag string, cmd *proto.UIDExpunge, mailbox *state.Mailbox, ch chan response.Response) (response.Response, error) {
	ctx = context2.AsUID(ctx)

	if mailbox.ReadOnly() {
		return nil, ErrReadOnly
	}

	if err := mailbox.Expunge(ctx, cmd.GetSequenceSet()); err != nil {
		return nil, err
	}

	if err := flush(ctx, mailbox, true, ch); err != nil {
		return nil, err
	}

	return response.Ok(tag).WithMessage("EXPUNGE"), nil
}
