package state

import (
	"bytes"
	"context"
	"fmt"
	"github.com/ProtonMail/gluon/internal/parser"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ProtonMail/gluon/imap"
	"github.com/ProtonMail/gluon/internal/contexts"
	"github.com/ProtonMail/gluon/internal/db"
	"github.com/ProtonMail/gluon/internal/db/ent"
	"github.com/ProtonMail/gluon/internal/parser/proto"
	"github.com/ProtonMail/gluon/rfc822"
	"github.com/bradenaw/juniper/parallel"
	"github.com/bradenaw/juniper/xslices"
	"golang.org/x/text/encoding"
)

var totalActiveSearchRequests int32

func (m *Mailbox) Search(ctx context.Context, keys []*proto.SearchKey, decoder *encoding.Decoder) ([]uint32, error) {
	var mapFn func(snapMsgWithSeq) uint32

	if contexts.IsUID(ctx) {
		mapFn = func(s snapMsgWithSeq) uint32 {
			return uint32(s.UID)
		}
	} else {
		mapFn = func(s snapMsgWithSeq) uint32 {
			return uint32(s.Seq)
		}
	}

	op, err := buildSearchOpListWithKeys(m, keys, decoder)
	if err != nil {
		return nil, err
	}

	msgCount := m.snap.len()

	result := make([]uint32, msgCount)

	activeSearchRequests := atomic.AddInt32(&totalActiveSearchRequests, 1)
	defer atomic.AddInt32(&totalActiveSearchRequests, -1)

	parallelism := runtime.NumCPU() / int(activeSearchRequests)

	if err := parallel.DoContext(ctx, parallelism, msgCount, func(ctx context.Context, i int) error {
		msg, ok := m.snap.messages.getWithSeqID(imap.SeqID(i + 1))
		if !ok {
			return nil
		}

		matches, err := applySearch(ctx, m, msg, op)
		if err != nil {
			return err
		}

		if matches {
			result[i] = mapFn(msg)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return xslices.Filter(result, func(v uint32) bool {
		return v != 0
	}), nil
}

func buildSearchData(ctx context.Context, m *Mailbox, op *buildSearchOpResult, message snapMsgWithSeq) (searchData, error) {
	data := searchData{message: message}

	if op.needsMessage {
		dbm, err := db.ReadResult(ctx, m.state.db(), func(ctx context.Context, client *ent.Client) (*ent.Message, error) {
			return db.GetMessageDateAndSize(ctx, client, message.ID.InternalID)
		})
		if err != nil {
			return searchData{}, err
		}

		data.dbMessage.size = dbm.Size
		data.dbMessage.date = dbm.Date
	}

	if op.needsLiteral {
		l, err := m.state.getLiteral(message.ID.InternalID)
		if err != nil {
			return searchData{}, err
		}

		data.literal = l
	}

	if op.needsHeader {
		headerBytes, _ := rfc822.Split(data.literal)
		h, err := rfc822.NewHeader(headerBytes)

		if err != nil {
			return searchData{}, err
		}

		data.header = h
	}

	return data, nil
}

func applySearch(ctx context.Context, m *Mailbox, msg snapMsgWithSeq, searchOp *buildSearchOpResult) (bool, error) {
	data, err := buildSearchData(ctx, m, searchOp, msg)
	if err != nil {
		return false, err
	}

	ok, err := searchOp.op(&data)
	if err != nil {
		return false, err
	}

	return ok, nil
}

type searchData struct {
	message   snapMsgWithSeq
	literal   []byte
	dbMessage struct {
		date time.Time
		size int
	}
	header *rfc822.Header
}

type searchOp = func(*searchData) (bool, error)

type buildSearchOpResult struct {
	op           searchOp
	needsLiteral bool
	needsMessage bool
	needsHeader  bool
}

func (b *buildSearchOpResult) merge(other *buildSearchOpResult) {
	b.needsLiteral = b.needsLiteral || other.needsLiteral
	b.needsMessage = b.needsMessage || other.needsMessage
	b.needsHeader = b.needsHeader || other.needsHeader
}

type searchOpResultOption interface {
	apply(*buildSearchOpResult)
}

type withHeaderSearchOpResultOption struct{}

func (withHeaderSearchOpResultOption) apply(s *buildSearchOpResult) {
	s.needsHeader = true
	s.needsLiteral = true
}

func needsHeader() searchOpResultOption {
	return &withHeaderSearchOpResultOption{}
}

type withLiteralSearchOpResultOption struct{}

func (withLiteralSearchOpResultOption) apply(s *buildSearchOpResult) {
	s.needsLiteral = true
}

func needsLiteral() searchOpResultOption {
	return &withLiteralSearchOpResultOption{}
}

type withDBMessageSearchOpResultOption struct{}

func (withDBMessageSearchOpResultOption) apply(s *buildSearchOpResult) {
	s.needsMessage = true
}

func needsDBMessage() searchOpResultOption {
	return &withDBMessageSearchOpResultOption{}
}

func newBuildSearchOpResult(op searchOp, needs ...searchOpResultOption) *buildSearchOpResult {
	r := &buildSearchOpResult{op: op}

	for _, v := range needs {
		v.apply(r)
	}

	return r
}

func buildSearchOp(m *Mailbox, key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	switch key.Keyword {
	case proto.SearchKeyword_SearchKWAll:
		return buildSearchOpAll()

	case proto.SearchKeyword_SearchKWAnswered:
		return buildSearchOpAnswered()

	case proto.SearchKeyword_SearchKWBcc:
		return buildSearchOpBcc(key, decoder)

	case proto.SearchKeyword_SearchKWBefore:
		return buildSearchOpBefore(key)

	case proto.SearchKeyword_SearchKWBody:
		return buildSearchOpBody(key, decoder)

	case proto.SearchKeyword_SearchKWCc:
		return buildSearchOpCc(key, decoder)

	case proto.SearchKeyword_SearchKWDeleted:
		return buildSearchOpDeleted()

	case proto.SearchKeyword_SearchKWDraft:
		return buildSearchOpDraft()

	case proto.SearchKeyword_SearchKWFlagged:
		return buildSearchOpFlagged()

	case proto.SearchKeyword_SearchKWFrom:
		return buildSearchOpFrom(key, decoder)

	case proto.SearchKeyword_SearchKWHeader:
		return buildSearchOpHeader(key, decoder)

	case proto.SearchKeyword_SearchKWKeyword:
		return buildSearchOpKeyword(key)

	case proto.SearchKeyword_SearchKWLarger:
		return buildSearchOpLarger(key)

	case proto.SearchKeyword_SearchKWNew:
		return buildSearchOpNew()

	case proto.SearchKeyword_SearchKWNot:
		return buildSearchOpNot(m, key, decoder)

	case proto.SearchKeyword_SearchKWOld:
		return buildSearchOpOld()

	case proto.SearchKeyword_SearchKWOn:
		return buildSearchOpOn(key)

	case proto.SearchKeyword_SearchKWOr:
		return buildSearchOpOr(m, key, decoder)

	case proto.SearchKeyword_SearchKWRecent:
		return buildSearchOpRecent()

	case proto.SearchKeyword_SearchKWSeen:
		return buildSearchOpSeen()

	case proto.SearchKeyword_SearchKWSentBefore:
		return buildSearchOpSentBefore(key)

	case proto.SearchKeyword_SearchKWSentOn:
		return buildSearchOpSentOn(key)

	case proto.SearchKeyword_SearchKWSentSince:
		return buildSearchOpSentSince(key)

	case proto.SearchKeyword_SearchKWSince:
		return buildSearchOpSince(key)

	case proto.SearchKeyword_SearchKWSmaller:
		return buildSearchOpSmaller(key)

	case proto.SearchKeyword_SearchKWSubject:
		return buildSearchOpSubject(key, decoder)

	case proto.SearchKeyword_SearchKWText:
		return buildSearchOpText(key, decoder)

	case proto.SearchKeyword_SearchKWTo:
		return buildSearchOpTo(key, decoder)

	case proto.SearchKeyword_SearchKWUID:
		return buildSearchOpUID(m, key)

	case proto.SearchKeyword_SearchKWUnanswered:
		return buildSearchOpUnanswered()

	case proto.SearchKeyword_SearchKWUndeleted:
		return buildSearchOpUndeleted()

	case proto.SearchKeyword_SearchKWUndraft:
		return buildSearchOpUndraft()

	case proto.SearchKeyword_SearchKWUnflagged:
		return buildSearchOpUnflagged()

	case proto.SearchKeyword_SearchKWUnkeyword:
		return buildSearchOpUnkeyword(key)

	case proto.SearchKeyword_SearchKWUnseen:
		return buildSearchOpUnseen()

	case proto.SearchKeyword_SearchKWSeqSet:
		return buildSearchOpSeqSet(m, key)

	case proto.SearchKeyword_SearchKWList:
		return buildSearchOpList(m, key, decoder)

	default:
		return nil, fmt.Errorf("bad search keyword")
	}
}

func buildSearchOpAll() (*buildSearchOpResult, error) {
	op := func(data *searchData) (bool, error) {
		return true, nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpAnswered() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagAnsweredLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpBcc(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyStr := strings.ToLower(string(decodedKey))

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("Bcc")

		return strings.Contains(strings.ToLower(value), decodedKeyStr), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpBefore(key *proto.SearchKey) (*buildSearchOpResult, error) {
	beforeDate, err := time.Parse("_2-Jan-2006", key.GetDate())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		return s.dbMessage.date.Before(beforeDate), nil
	}

	return newBuildSearchOpResult(op, needsDBMessage()), nil
}

func buildSearchOpBody(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	keyBytes, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	keyBytesLower := bytes.ToLower(keyBytes)

	op := func(s *searchData) (bool, error) {
		section := rfc822.Parse(s.literal)

		return bytes.Contains(bytes.ToLower(section.Body()), keyBytesLower), nil
	}

	return newBuildSearchOpResult(op, needsLiteral()), nil
}

func buildSearchOpCc(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyStr := strings.ToLower(string(decodedKey))

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("Cc")

		return strings.Contains(strings.ToLower(value), decodedKeyStr), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpDeleted() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagDeletedLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpDraft() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagDraftLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpFlagged() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagFlaggedLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpFrom(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyStr := strings.ToLower(string(decodedKey))

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("From")

		return strings.Contains(strings.ToLower(value), decodedKeyStr), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpHeader(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyStr := strings.ToLower(string(decodedKey))

	op := func(s *searchData) (bool, error) {
		value := s.header.Get(key.GetField())

		return strings.Contains(strings.ToLower(value), decodedKeyStr), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpKeyword(key *proto.SearchKey) (*buildSearchOpResult, error) {
	flagLowerCase := strings.ToLower(key.GetFlag())

	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(flagLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpLarger(key *proto.SearchKey) (*buildSearchOpResult, error) {
	size := int(key.GetSize())

	op := func(s *searchData) (bool, error) {
		return s.dbMessage.size > size, nil
	}

	return newBuildSearchOpResult(op, needsDBMessage()), nil
}

func buildSearchOpNew() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagRecentLowerCase) && !s.message.flags.ContainsUnchecked(imap.FlagSeenLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpNot(m *Mailbox, key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	toNegateOpResult, err := buildSearchOp(m, key.GetLeftOp(), decoder)
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		result, err := toNegateOpResult.op(s)
		if err != nil {
			return false, err
		}

		return !result, nil
	}

	result := newBuildSearchOpResult(op)
	result.merge(toNegateOpResult)

	return result, nil
}

func buildSearchOpOld() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(imap.FlagRecentLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpOn(key *proto.SearchKey) (*buildSearchOpResult, error) {
	onDate, err := time.Parse("_2-Jan-2006", key.GetDate())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		return onDate.Truncate(24 * time.Hour).Equal(s.dbMessage.date.Truncate(24 * time.Hour)), nil
	}

	return newBuildSearchOpResult(op, needsDBMessage()), nil
}

func buildSearchOpOr(m *Mailbox, key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	leftOp, err := buildSearchOp(m, key.GetLeftOp(), decoder)
	if err != nil {
		return nil, err
	}

	rightOp, err := buildSearchOp(m, key.GetRightOp(), decoder)
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		leftResult, err := leftOp.op(s)
		if err != nil {
			return false, err
		}

		rightResult, err := rightOp.op(s)
		if err != nil {
			return false, err
		}

		return leftResult || rightResult, nil
	}

	result := newBuildSearchOpResult(op)
	result.merge(leftOp)
	result.merge(rightOp)

	return result, nil
}

func buildSearchOpRecent() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagRecentLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpSeen() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return s.message.flags.ContainsUnchecked(imap.FlagSeenLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpSentBefore(key *proto.SearchKey) (*buildSearchOpResult, error) {
	beforeDate, err := time.Parse("_2-Jan-2006", key.GetDate())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("Date")

		date, err := parser.ParseRFC5322DateTime(value)
		if err != nil {
			return false, err
		}

		date = convertToDateWithoutTZ(date)

		return date.Before(beforeDate), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpSentOn(key *proto.SearchKey) (*buildSearchOpResult, error) {
	onDate, err := time.Parse("_2-Jan-2006", key.GetDate())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("Date")

		date, err := parser.ParseRFC5322DateTime(value)
		if err != nil {
			return false, err
		}

		// GODT-1598: Is this correct?
		date = convertToDateWithoutTZ(date)

		return date.Equal(onDate), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpSentSince(key *proto.SearchKey) (*buildSearchOpResult, error) {
	sinceDate, err := time.Parse("_2-Jan-2006", key.GetDate())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("Date")

		date, err := parser.ParseRFC5322DateTime(value)
		if err != nil {
			return false, err
		}

		date = convertToDateWithoutTZ(date)

		return date.Equal(sinceDate) || date.After(sinceDate), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpSince(key *proto.SearchKey) (*buildSearchOpResult, error) {
	sinceDate, err := time.Parse("_2-Jan-2006", key.GetDate())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		date := convertToDateWithoutTZ(s.dbMessage.date)

		return date.Equal(sinceDate) || date.After(sinceDate), nil
	}

	return newBuildSearchOpResult(op, needsDBMessage()), nil
}

func buildSearchOpSmaller(key *proto.SearchKey) (*buildSearchOpResult, error) {
	size := int(key.GetSize())

	op := func(s *searchData) (bool, error) {
		return s.dbMessage.size < size, nil
	}

	return newBuildSearchOpResult(op, needsDBMessage()), nil
}

func buildSearchOpSubject(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyStr := strings.ToLower(string(decodedKey))

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("Subject")

		return strings.Contains(strings.ToLower(value), decodedKeyStr), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpText(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyLower := bytes.ToLower(decodedKey)

	op := func(s *searchData) (bool, error) {
		return bytes.Contains(bytes.ToLower(s.literal), decodedKeyLower), nil
	}

	return newBuildSearchOpResult(op, needsLiteral()), nil
}

func buildSearchOpTo(key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	decodedKey, err := decoder.Bytes(key.GetText())
	if err != nil {
		return nil, err
	}

	decodedKeyStr := strings.ToLower(string(decodedKey))

	op := func(s *searchData) (bool, error) {
		value := s.header.Get("To")

		return strings.Contains(strings.ToLower(value), decodedKeyStr), nil
	}

	return newBuildSearchOpResult(op, needsHeader()), nil
}

func buildSearchOpUID(m *Mailbox, key *proto.SearchKey) (*buildSearchOpResult, error) {
	intervals, err := m.snap.resolveUIDInterval(key.GetSequenceSet())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		for _, v := range intervals {
			if v.contains(s.message.UID) {
				return true, nil
			}
		}

		return false, nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpUnanswered() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(imap.FlagAnsweredLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpUndeleted() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(imap.FlagDeletedLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpUndraft() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(imap.FlagDraftLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpUnflagged() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(imap.FlagFlaggedLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpUnkeyword(key *proto.SearchKey) (*buildSearchOpResult, error) {
	flagLowerCase := strings.ToLower(key.GetFlag())

	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(flagLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpUnseen() (*buildSearchOpResult, error) {
	op := func(s *searchData) (bool, error) {
		return !s.message.flags.ContainsUnchecked(imap.FlagSeenLowerCase), nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpSeqSet(m *Mailbox, key *proto.SearchKey) (*buildSearchOpResult, error) {
	intervals, err := m.snap.resolveSeqInterval(key.GetSequenceSet())
	if err != nil {
		return nil, err
	}

	op := func(s *searchData) (bool, error) {
		for _, v := range intervals {
			if v.contains(s.message.Seq) {
				return true, nil
			}
		}

		return false, nil
	}

	return newBuildSearchOpResult(op), nil
}

func buildSearchOpList(m *Mailbox, key *proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	opKeys := key.GetChildren()

	return buildSearchOpListWithKeys(m, opKeys, decoder)
}

func buildSearchOpListWithKeys(m *Mailbox, opKeys []*proto.SearchKey, decoder *encoding.Decoder) (*buildSearchOpResult, error) {
	ops := make([]searchOp, 0, len(opKeys))

	opResult := newBuildSearchOpResult(nil)

	for _, opKey := range opKeys {
		result, err := buildSearchOp(m, opKey, decoder)
		if err != nil {
			return nil, err
		}

		opResult.merge(result)
		ops = append(ops, result.op)
	}

	opResult.op = func(s *searchData) (bool, error) {
		result := true

		for _, v := range ops {
			ok, err := v(s)
			if err != nil {
				return false, err
			}

			result = result && ok

			if !result {
				break
			}
		}

		return result, nil
	}

	return opResult, nil
}

func convertToDateWithoutTZ(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
