package codexbroker

import (
	"context"
	"encoding/json"
	"time"
)

type transferRequest struct {
	Action  string
	Receipt TransferReceipt
}

func encodeTransferRequest(action string, receipt TransferReceipt) (json.RawMessage, error) {
	return json.Marshal(transferRequest{action, receipt})
}
func decodeTransferReceipt(raw json.RawMessage, receipt *TransferReceipt) error {
	return json.Unmarshal(raw, receipt)
}

func (s *session) handleTransfer(request wireRequest) {
	var input transferRequest
	if json.Unmarshal(request.Params, &input) != nil || input.Receipt.Source.RuntimeID != s.host.runtimeID || input.Receipt.Source.Thread != request.Thread || input.Receipt.Source.Endpoint != s.host.discovery.Endpoint() {
		s.refuse(request.ID, RefusalFrameInvalid)
		return
	}
	budget := 10 * time.Second
	if input.Action == "retire" || input.Action == "native-resume" {
		budget = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	defer func() {
		select {
		case <-s.departed:
			s.host.broker.abandonTransfers(s.id)
		default:
		}
	}()
	go func() {
		select {
		case <-s.departed:
			cancel()
		case <-ctx.Done():
		}
	}()
	receipt := input.Receipt
	var err error
	switch input.Action {
	case "prepare":
		if reason := s.host.refusingWork(); reason != RefusalNone {
			s.refuse(request.ID, reason)
			return
		}
		receipt, err = s.host.broker.PrepareTransfer(ctx, receipt.Source, s.id, receipt.Token)
	case "inspect":
		receipt, err = s.host.broker.InspectTransfer(receipt)
	case "reclaim":
		receipt, err = s.host.broker.ReclaimTransfer(receipt, s.id)
	case "native-resume":
		var native nativeResumeRequest
		if json.Unmarshal(request.Params, &native) != nil {
			s.refuse(request.ID, RefusalFrameInvalid)
			return
		}
		binding, resumeErr := s.host.broker.resumeNativeTransfer(ctx, native, s.id)
		if resumeErr != nil {
			reason := RefusalOf(resumeErr)
			if reason == RefusalNone {
				reason = RefusalEndpointRefused
			}
			s.refuse(request.ID, reason)
			return
		}
		raw, _ := json.Marshal(binding)
		s.reply(request.ID, wireReply{Kind: replyResult, Result: raw})
		return
	case "native-activate":
		if receipt.NativeTarget == nil {
			err = refuse(RefusalFrameInvalid, nil)
		} else {
			err = s.host.broker.activateNativeTransfer(receipt, s.id, *receipt.NativeTarget)
		}
	case "native-grant":
		if receipt.NativeTarget == nil {
			err = refuse(RefusalFrameInvalid, nil)
		} else {
			err = s.host.broker.grantNativeTransfer(receipt, s.id, *receipt.NativeTarget)
		}
	case "check":
		receipt, err = s.host.broker.CheckTransfer(receipt, s.id)
	case "retire":
		receipt, err = s.host.broker.RetireTransfer(ctx, receipt, s.id)
	case "finish":
		err = s.host.broker.finishTransfer(receipt, s.id, true)
	case "abort":
		err = s.host.broker.finishTransfer(receipt, s.id, false)
	default:
		err = refuse(RefusalRequestUnknown, nil)
	}
	if err != nil {
		reason := RefusalOf(err)
		if reason == RefusalNone {
			reason = RefusalEndpointRefused
		}
		s.refuse(request.ID, reason)
		return
	}
	if input.Action == "finish" || input.Action == "abort" {
		s.host.transferReleased()
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		s.refuse(request.ID, RefusalFrameInvalid)
		return
	}
	s.reply(request.ID, wireReply{Kind: replyResult, Result: raw})
}
