package codexbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// TransferSource is the existing source's exact recipe and broker authority.
// It allocates neither a new binding nor a process-host identity.
type TransferSource struct {
	Project, Window, Agent, Pane, Generation, Operation, RuntimeID, Thread string
	PaneRuntimeID                                                          string
	Fence                                                                  Fence
	Endpoint                                                               EndpointKey
}

func (s TransferSource) valid() bool {
	return s.Project != "" && s.Window != "" && s.Agent != "" && s.Pane != "" && s.Generation != "" && s.Operation != "" && s.RuntimeID != "" && s.PaneRuntimeID != "" && s.Endpoint != "" && s.Thread != "" && s.Fence.Connection != 0 && s.Fence.Binding != 0
}

type TransferReceipt struct {
	Peer         codexappserver.PeerIdentity
	Token        string
	Source       TransferSource
	Retired      bool
	NativeTarget *NativeTransferTarget
	Completed    bool
}

type threadTransfer struct {
	receipt                             TransferReceipt
	owner                               string
	conn                                *connection
	binding                             *Binding
	nativeTarget                        *NativeTransferTarget
	nativeBinding                       *Binding
	nativeInit, nativeReady             bool
	drain                               chan struct{}
	draining, attempted, busy, orphaned bool
}

// PrepareTransfer freezes the existing exact binding and drains already
// admitted operations without holding the broker mutex. The reservation pins
// the captured connection even after the last ordinary Binding.Close.
func (b *Broker) PrepareTransfer(ctx context.Context, source TransferSource, owner, token string) (TransferReceipt, error) {
	decoded, tokenErr := hex.DecodeString(token)
	if !source.valid() || owner == "" || tokenErr != nil || len(decoded) != 32 {
		return TransferReceipt{}, refuse(RefusalFrameInvalid, nil)
	}
	b.mu.Lock()
	bd := b.bindings[source.Thread]
	if bd == nil {
		b.mu.Unlock()
		return TransferReceipt{}, refuse(RefusalBindingClosed, nil)
	}
	if _, found := b.transfers[source.Thread]; found {
		b.mu.Unlock()
		return TransferReceipt{}, refuse(RefusalControlNotOpen, nil)
	}
	conn, err := bd.authorityLocked(source.Fence)
	if err != nil {
		b.mu.Unlock()
		return TransferReceipt{}, err
	}
	if !conn.peer.Valid() {
		b.mu.Unlock()
		return TransferReceipt{}, refuse(RefusalLifecycleUnsupported, nil)
	}
	t := &threadTransfer{receipt: TransferReceipt{Token: token, Source: source, Peer: conn.peer}, owner: owner, conn: conn, binding: bd, drain: make(chan struct{}), draining: true}
	b.transfers[source.Thread] = t
	if bd.inflight == 0 {
		close(t.drain)
		t.draining = false
	}
	b.mu.Unlock()
	b.signal()
	select {
	case <-ctx.Done():
		_ = b.finishTransfer(t.receipt, owner, false)
		return TransferReceipt{}, ctx.Err()
	case <-b.done:
		return TransferReceipt{}, refuse(RefusalBrokerClosed, nil)
	case <-t.drain:
	}
	b.mu.Lock()
	err = b.transferCurrentLocked(t)
	receipt := t.receipt
	b.mu.Unlock()
	if err != nil {
		return TransferReceipt{}, err
	}
	return receipt, nil
}

func (b *Broker) transferCurrentLocked(t *threadTransfer) error {
	if b.closing || b.conn != t.conn || t.conn.peer != t.receipt.Peer || t.orphaned {
		return refuse(RefusalStaleConnectionEpoch, nil)
	}
	return nil
}
func (b *Broker) transferLocked(receipt TransferReceipt, owner string) (*threadTransfer, error) {
	t := b.transfers[receipt.Source.Thread]
	if t == nil || t.receipt.Token != receipt.Token || t.receipt.Source != receipt.Source || t.receipt.Peer != receipt.Peer || t.owner != owner {
		return nil, refuse(RefusalLeaseIdentityMismatch, nil)
	}
	return t, nil
}

// RetireTransfer runs unsubscribe and the absence barrier on the captured
// connection. Any uncertain result leaves the same-thread fence in place.
func (b *Broker) RetireTransfer(ctx context.Context, receipt TransferReceipt, owner string) (TransferReceipt, error) {
	b.mu.Lock()
	t, err := b.transferLocked(receipt, owner)
	if err == nil {
		err = b.transferCurrentLocked(t)
	}
	if err == nil && (t.busy || t.draining || t.binding.inflight != 0) {
		err = refuse(RefusalControlNotOpen, nil)
	}
	if err != nil {
		b.mu.Unlock()
		return TransferReceipt{}, err
	}
	t.busy = true
	t.attempted = true
	b.mu.Unlock()
	defer func() { b.mu.Lock(); t.busy = false; b.mu.Unlock() }()
	guard := func() error { b.mu.Lock(); defer b.mu.Unlock(); return b.transferCurrentLocked(t) }
	if _, err = codexappserver.UnsubscribeThread(ctx, t.conn.endpoint, receipt.Source.Thread); err != nil {
		return TransferReceipt{}, err
	}
	if err = codexappserver.AwaitThreadRetirement(ctx, t.conn.endpoint, receipt.Source.Thread, guard); err != nil {
		return TransferReceipt{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err = b.transferCurrentLocked(t); err != nil {
		return TransferReceipt{}, err
	}
	t.receipt.Retired = true
	return t.receipt, nil
}

// FinishTransfer releases only the exact request-owned reservation. Before
// unsubscribe, abort restores a surviving old binding; after unsubscribe only
// a verified target handoff or explicit recovery after target Wait may finish.
func (b *Broker) finishTransfer(receipt TransferReceipt, owner string, handoff bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, err := b.transferLocked(receipt, owner)
	if err != nil {
		return err
	}
	if t.busy || t.draining || t.orphaned || (t.attempted && (!handoff || !t.receipt.Retired)) {
		return refuse(RefusalControlNotOpen, nil)
	}
	if err = b.transferCurrentLocked(t); err != nil {
		return err
	}
	if !handoff && (t.binding.revoked != RefusalNone || b.bindings[receipt.Source.Thread] != t.binding) {
		return refuse(RefusalBindingClosed, nil)
	}
	if t.attempted {
		if t.nativeTarget != nil && (t.nativeBinding == nil || t.nativeBinding.revoked != RefusalNone || t.nativeBinding.stage != stageOpen || t.nativeBinding.conn != t.conn) {
			return refuse(RefusalControlNotOpen, nil)
		}
		b.revokeBindingLocked(t.binding, RefusalBindingClosed)
	}
	if t.attempted {
		finished := t.receipt
		finished.Completed = true
		b.completedTransfers[finished.Token] = finished
		b.transferCompletions = append(b.transferCompletions, finished.Token)
		if len(b.transferCompletions) > 64 {
			delete(b.completedTransfers, b.transferCompletions[0])
			b.transferCompletions = b.transferCompletions[1:]
		}
	}
	delete(b.transfers, receipt.Source.Thread)
	b.signal()
	return nil
}

// Abandoned requests retain their fences. A vanished coordinator cannot prove
// target Wait or authorize a replacement writer.
func (b *Broker) abandonTransfers(owner string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range b.transfers {
		if t.owner == owner {
			t.orphaned = true
		}
	}
}

// ReclaimTransfer transfers an orphaned request to a new coordinator carrying
// the exact owner-private receipt. It does not release the fence, mint control
// authority, or assert that a target writer exited. The app must check its
// durable target Wait and latest source/target CAS before any handoff/recovery.
// An epoch replacement may be qualified only on the same kernel endpoint peer.
func (b *Broker) ReclaimTransfer(receipt TransferReceipt, owner string) (TransferReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.transfers[receipt.Source.Thread]
	if owner == "" || t == nil || t.receipt.Token != receipt.Token || t.receipt.Source != receipt.Source || t.receipt.Peer != receipt.Peer {
		return TransferReceipt{}, refuse(RefusalLeaseIdentityMismatch, nil)
	}
	if t.nativeBinding != nil && t.nativeBinding.revoked == RefusalNone {
		return TransferReceipt{}, refuse(RefusalControlNotOpen, nil)
	}
	if !t.orphaned || t.busy || t.draining || t.binding.inflight != 0 || b.closing || b.conn == nil || b.conn.peer != t.receipt.Peer {
		return TransferReceipt{}, refuse(RefusalControlNotOpen, nil)
	}
	// Preserve source epochs as historical identity; new connection authority is
	// internal to this reservation and cannot authorize an old binding token.
	if b.conn != t.conn {
		t.conn = b.conn
		t.receipt.Retired = false
	}
	t.nativeTarget, t.nativeBinding = nil, nil
	t.nativeInit, t.nativeReady = false, false
	t.owner, t.orphaned = owner, false
	return t.receipt, nil
}

// CheckTransfer is read-only reservation authority, including the peer and
// closed retirement barrier. It neither resumes nor snapshots the thread.
func (b *Broker) CheckTransfer(receipt TransferReceipt, owner string) (TransferReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, err := b.transferLocked(receipt, owner)
	if err != nil {
		return TransferReceipt{}, err
	}
	if err = b.transferCurrentLocked(t); err != nil {
		return TransferReceipt{}, err
	}
	if t.busy || t.draining {
		return TransferReceipt{}, refuse(RefusalControlNotOpen, nil)
	}
	return t.receipt, nil
}

func (bd *Binding) admittedDone() {
	b := bd.broker
	b.mu.Lock()
	defer b.mu.Unlock()
	bd.inflight--
	if t := b.transfers[bd.threadID]; t != nil && t.binding == bd && t.draining && bd.inflight == 0 {
		t.draining = false
		close(t.drain)
	}
}

// RemoteTransfer owns a disposable client connection, independent of the old
// watcher binding whose Close is part of tmux retirement.
type RemoteTransfer struct {
	conn    *Conn
	receipt TransferReceipt
	once    sync.Once
}

func (c *Conn) PrepareTransfer(ctx context.Context, receipt TransferReceipt) (*RemoteTransfer, error) {
	source := receipt.Source
	if source.RuntimeID != c.runtime {
		return nil, refuse(RefusalRuntimeReplaced, nil)
	}
	reply, err := c.transferCall(ctx, "prepare", receipt)
	if err != nil {
		return nil, err
	}
	return &RemoteTransfer{conn: c, receipt: reply}, nil
}
func (c *Conn) transferCall(ctx context.Context, action string, receipt TransferReceipt) (TransferReceipt, error) {
	params, err := encodeTransferRequest(action, receipt)
	if err != nil {
		return TransferReceipt{}, err
	}
	reply, err := c.call(ctx, wireRequest{Kind: requestTransfer, Thread: receipt.Source.Thread, Params: params})
	if err != nil {
		return TransferReceipt{}, err
	}
	if reply.Kind == replyRefused {
		return TransferReceipt{}, refuse(reply.Refusal, nil)
	}
	var got TransferReceipt
	if err = decodeTransferReceipt(reply.Result, &got); err != nil {
		return TransferReceipt{}, err
	}
	return got, nil
}
func (t *RemoteTransfer) Retire(ctx context.Context) error {
	receipt, err := t.conn.transferCall(ctx, "retire", t.receipt)
	if err == nil {
		t.receipt = receipt
	}
	return err
}
func (t *RemoteTransfer) Finish(ctx context.Context) error {
	_, err := t.conn.transferCall(ctx, "finish", t.receipt)
	return err
}
func (t *RemoteTransfer) Abort(ctx context.Context) error {
	_, err := t.conn.transferCall(ctx, "abort", t.receipt)
	return err
}
func (t *RemoteTransfer) Receipt() TransferReceipt { return t.receipt }
func (t *RemoteTransfer) Close() error {
	var err error
	t.once.Do(func() { err = t.conn.Close() })
	return err
}

// ReclaimTransfer never binds a thread; a durable receipt is required.
func (c *Conn) ReclaimTransfer(ctx context.Context, receipt TransferReceipt) (*RemoteTransfer, error) {
	if receipt.Source.RuntimeID != c.runtime {
		return nil, refuse(RefusalRuntimeReplaced, nil)
	}
	got, err := c.transferCall(ctx, "reclaim", receipt)
	if err != nil {
		return nil, err
	}
	return &RemoteTransfer{conn: c, receipt: got}, nil
}
func (t *RemoteTransfer) Check(ctx context.Context) error {
	got, err := t.conn.transferCall(ctx, "check", t.receipt)
	if err == nil {
		t.receipt = got
	}
	return err
}

func (b *Broker) hasTransfers() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.transfers) > 0 }

// DialTransfer reaches an existing runtime only. Its session may perform only
// exact request-owned transfer operations, including recovery during drain.
func DialTransfer(ctx context.Context, discovery Discovery, cfg DialConfig) (*Conn, error) {
	return dial(ctx, discovery, cfg, transferSessionPurpose)
}

// InspectTransfer recognizes a bounded exact completion receipt after a lost
// handoff ACK. Absence is unknown, never proof of completion or retirement.
func (b *Broker) InspectTransfer(receipt TransferReceipt) (TransferReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.transfers[receipt.Source.Thread]; t != nil && t.receipt.Token == receipt.Token && t.receipt.Source == receipt.Source && (t.receipt.Peer == receipt.Peer || receipt.Peer == (codexappserver.PeerIdentity{})) {
		return t.receipt, nil
	}
	if got, ok := b.completedTransfers[receipt.Token]; ok && got.Source == receipt.Source && got.Peer == receipt.Peer {
		return got, nil
	}
	return TransferReceipt{}, refuse(RefusalLeaseIdentityMismatch, nil)
}
func (c *Conn) InspectTransfer(ctx context.Context, receipt TransferReceipt) (TransferReceipt, error) {
	if receipt.Source.RuntimeID != c.runtime {
		return TransferReceipt{}, refuse(RefusalRuntimeReplaced, nil)
	}
	return c.transferCall(ctx, "inspect", receipt)
}

// NewTransferReceipt creates request identity before broker admission, allowing
// the caller to persist it before a lost prepare ACK or coordinator death.
func NewTransferReceipt(source TransferSource) (TransferReceipt, error) {
	if !source.valid() {
		return TransferReceipt{}, refuse(RefusalFrameInvalid, nil)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return TransferReceipt{}, err
	}
	return TransferReceipt{Source: source, Token: hex.EncodeToString(secret[:])}, nil
}
