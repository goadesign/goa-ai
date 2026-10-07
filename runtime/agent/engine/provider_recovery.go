package engine

// Provider recovery carries certified planner failures through actual workflow
// parents. The runtime owns allowance decisions and retains the planner input;
// the engine owns child identity, ordered delivery, and replay. These contracts
// expose neither native execution addresses nor delivery records.

import (
	"time"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// ProviderRecoveryPort connects one workflow to its actual parent and children.
	// Engines preserve this control across WithCancel and Detached. Callbacks run
	// as serialized workflow operations, including while a workflow awaits work.
	// They must not run concurrently with ordinary workflow code. In-memory
	// engines dispatch callbacks at workflow wait points, including Future.Get
	// and blocking Execute methods; a callback-only mutex is insufficient.
	ProviderRecoveryPort interface {
		// HasParent reports whether this execution inherited provider recovery
		// from its actual parent. A root or an unscoped execution returns false.
		// Malformed inherited control is an error when constructing the context;
		// it must not be treated as an unscoped execution.
		HasParent() bool

		// Register installs the runtime receiver before child dispatch. Children
		// subsequently accepted by this execution inherit its recovery control.
		// Native wrapper workflows forward inherited requests until a runtime
		// installs this receiver; applications need no separate forwarding call.
		// A second registration returns an error.
		Register(ProviderRecoveryAccept) error

		// Open records an obligation to deliver one local certified failure to
		// the actual parent and binds replies to receive. It requires HasParent.
		// The returned control orders later sends after the parent's acceptance.
		// Delivery failure reaches receive as ProviderRecoveryStopped. Returning
		// a control does not by itself authorize a wait or another attempt.
		Open(WorkflowContext, ProviderRecoveryFailure, ProviderRecoveryReceive) (ProviderRecovery, error)

		// RegisterPauseHandler installs the receiver for measured child pauses
		// before child dispatch. It does not enable recovery or allocate an
		// allowance. A second registration returns an error. Until registration,
		// a native wrapper retains accepted child reports but claims no pause
		// for itself merely because a descendant reported one.
		RegisterPauseHandler(ProviderRecoveryPauseAccept) error

		// ReportPause records this execution's measured pause for its actual
		// issuing parent, independently of any failed planner request. HasParent
		// describes inherited recovery ownership and does not restrict reporting.
		// Without an engine-issued parent binding there is no parent clock to
		// update, and the returned future completes without delivery. A malformed
		// or failed existing binding returns an error; it is never treated as
		// absent. Parent identity is not supplied by the caller.
		//
		// Reports use the same ordered delivery as this child's recovery sends,
		// across all its requests. Exact repeats keep their original acceptance;
		// conflicting delivery repeats fail. The future completes after the
		// parent's receiver applies the report and records any onward report.
		// Native signal delivery alone is not acceptance. Ordinary completion
		// waits for these futures; cancellation preserves the first terminal
		// decision and does not wait indefinitely for an unreachable parent.
		ReportPause(WorkflowContext, ProviderRecoveryPaused) (Future[struct{}], error)
	}

	// ProviderRecovery is one unfinished planner request and its two-way control.
	// The engine binds it to the original requester and first failed publication.
	// Relays cannot replace that identity with their own execution or input.
	ProviderRecovery interface {
		// Forward records delivery of an incoming failure to this workflow's
		// actual parent. Replies use receive; later messages retain the original
		// request binding. Only an incoming control may be forwarded.
		Forward(WorkflowContext, ProviderRecoveryReceive) (ProviderRecovery, error)

		// Send records an ordered outgoing transition without blocking a receiver.
		// The future completes only after the immediate receiver accepts the
		// transition and records its outgoing obligations. Native signal delivery
		// alone is insufficient. Exact delivery repeats return the original
		// acceptance; conflicting repeats fail. Runtime phase settlement is a
		// separate ProviderRecoverySettled message from the allowance owner.
		//
		// Engines retain accepted sends through replay. Workflows must drain
		// their accepted obligations before ordinary completion. A failed future
		// stops recovery; it never permits another attempt.
		//
		// Wire encoding preserves canonical provider fields, cause presence and
		// text, and errors.Is classification for context.Canceled,
		// context.DeadlineExceeded, and ErrPlannerActivityDeadlineExceeded.
		// It does not promise Go error object identity across processes.
		Send(WorkflowContext, ProviderRecoveryMessage) (Future[struct{}], error)
	}

	// ProviderRecoveryAccept receives a child's first certified failure and
	// returns the receiver for that unfinished planner request. The engine
	// supplies childWorkflowID from its accepted ChildWorkflowRequest and issued
	// handle, including when that child relays a deeper failure. The first
	// accepted native execution anchors that logical child's chain of attempts.
	// Each private delivery retains its originating native attempt for ordering
	// and replay; an earlier attempt's measured interval stays with that attempt.
	//
	// Within the trusted workflow namespace, the engine checks the issued-child
	// capability, accepted chain anchor, and actual parent namespace/execution
	// binding before invoking this callback. A signal's asserted sender ID is
	// insufficient, and this contract does not claim protection from namespace
	// administrators who can read execution history. The callback ID is never
	// taken from model input or guessed from the latest child attempt.
	// A later native attempt does not create a fresh root allowance.
	// An early request waits for child acceptance and handle binding. The callback
	// records forwarding or an owner decision before returning, without waiting
	// for a reply. The ID also avoids requiring comparable Go handle objects.
	ProviderRecoveryAccept func(
		wfCtx WorkflowContext,
		childWorkflowID string,
		failure ProviderRecoveryFailure,
		control ProviderRecovery,
	) (ProviderRecoveryReceive, error)

	// ProviderRecoveryReceive applies one transition to an accepted request.
	// It records state and any outgoing obligations before returning, without
	// waiting for a reply. Its error rejects the transition and stops recovery.
	ProviderRecoveryReceive func(WorkflowContext, ProviderRecoveryMessage) error

	// ProviderRecoveryPauseAccept applies one immediate child's measured pause.
	// The engine derives childWorkflowID from the accepted logical child chain,
	// retaining the native origin and trust checks of ProviderRecoveryAccept. Reports
	// arriving before that binding wait for acceptance. The runtime unions them
	// for that child's lifetime, including overlap across failed requests, then
	// computes its own whole-workflow pause before reporting to its parent.
	// Accepted reports run before a subsequent deadline decision; reports after
	// a winning terminal decision cannot change that decision. Returning an error
	// rejects the report. This callback never waits for onward acceptance.
	ProviderRecoveryPauseAccept func(
		wfCtx WorkflowContext,
		childWorkflowID string,
		pause ProviderRecoveryPaused,
	) error

	// ProviderRecoveryFailure certifies one completed failed planner activity,
	// including queueing, preparation, finalization, and publication. The runtime
	// constructs it only after validating the complete model invocation result.
	// It says nothing about which part of that interval was provider execution.
	ProviderRecoveryFailure struct {
		// PublicationBatchID identifies this failed activity's completed publication.
		PublicationBatchID string
		// StartedAt is workflow time immediately before scheduling the activity.
		StartedAt time.Time
		// EndedAt is workflow time after validating and publishing its result.
		EndedAt time.Time
		// Err is the certified, retryable canonical provider error.
		Err *model.ProviderError
	}

	// ProviderRecoveryMessage is a closed set of recovery transitions. Engines
	// carry these values; the runtime checks direction and phase ordering.
	// Delivery sequence, request address, and acknowledgment stay engine-private.
	ProviderRecoveryMessage interface {
		providerRecoveryMessage()
	}

	// ProviderWaitRequest asks the owner to allow the next backoff for this
	// failed request. Healthy initial attempts do not request any permission.
	ProviderWaitRequest struct {
		// Delay is the positive backoff computed by the requesting runtime.
		Delay time.Duration
	}

	// ProviderAttemptRequest asks the owner to allow another attempt of the
	// retained planner input. Activity options stay with the requesting runtime.
	ProviderAttemptRequest struct {
		// ActiveDeadline is the requester's current active-work deadline.
		// Zero retains the existing unlimited-active-time meaning.
		ActiveDeadline time.Time
	}

	// ProviderRecoveryPermission allows exactly the outstanding wait or attempt.
	// Receipt earns no time credit. Before entering the phase the requester
	// checks cancellation and expiry; it never renews this permission.
	ProviderRecoveryPermission struct {
		// ExpiresAt is the finite, nonzero absolute end of the allowed phase.
		// At or after this time, a phase that has not entered must not start.
		ExpiresAt time.Time
	}

	// ProviderWaitObserved reports an actually entered timer's measured prefix.
	// Equal endpoints prove entry but earn no credit. Repeated coverage is
	// counted once, including overlap with other certified failures or waits.
	ProviderWaitObserved struct {
		// StartedAt is workflow time immediately before entering the timer.
		StartedAt time.Time
		// Through is the latest observed workflow time while that timer waited.
		Through time.Time
	}

	// ProviderWaitFinished reports the actual timer interval, including any
	// observed overshoot. It closes the wait and releases proven unused capacity.
	ProviderWaitFinished struct {
		// StartedAt is the same timer entry reported in ProviderWaitObserved.
		StartedAt time.Time
		// EndedAt is workflow time when the timer wait returned.
		EndedAt time.Time
	}

	// ProviderWaitObservationRequest asks for the measured prefix of an entered
	// timer or its retained closure. It neither starts a timer nor extends one.
	ProviderWaitObservationRequest struct{}

	// ProviderRecoveryPaused reports measured active-clock credit through
	// ReportPause, separately from one failed request's messages. The sender
	// proves its own completed failed activity or entered backoff, or time it
	// waited with every unfinished branch proven paused. Healthy or unresolved
	// parallel work and local publication outside its own certified failure
	// remain active. The half-open interval includes StartedAt and excludes
	// Through; equal endpoints earn no credit. Future endpoints are invalid.
	// This value never spends or refills allowance, or renews a permission.
	//
	// A parent consumes this value for its child's clock, then computes its own
	// paused coverage before sending another value upward. Neither a runtime
	// relay nor a native wrapper may forward this value unchanged as its own
	// credit. A wrapper with no such evidence supplies no paused interval.
	ProviderRecoveryPaused struct {
		// StartedAt is the beginning of the measured blocked interval.
		StartedAt time.Time
		// Through is the observed end, never an unelapsed permission expiry.
		Through time.Time
	}

	// ProviderAttemptSucceeded closes a successful retry without charging its
	// time to recovery. The requester waits for owner settlement before returning.
	ProviderAttemptSucceeded struct{}

	// ProviderAttemptFailed closes a retry with a fresh completed certificate.
	ProviderAttemptFailed struct {
		// Failure retains the whole failed activity interval, even if late
		// publication places its end after the issued permission's expiry.
		Failure ProviderRecoveryFailure
	}

	// ProviderPhaseUnused proves that the permitted timer or activity was never
	// entered. It releases the hold without inventing a failure interval.
	ProviderPhaseUnused struct{}

	// ProviderPhaseFailed reports a native or unproven phase failure. An expiry
	// or lost reply alone cannot prove unused capacity or authorize a retry.
	ProviderPhaseFailed struct {
		// Err is the actual failure; it must be non-nil.
		Err error
	}

	// ProviderRecoverySettled confirms that the owner recorded the phase outcome.
	// Only after this message may the requester ask for the next phase or finish
	// a successful retry. Acceptance by an intermediate parent is insufficient.
	ProviderRecoverySettled struct{}

	// ProviderRecoveryStopped preserves the first winning terminal decision.
	// It travels toward the requester when the owner stops recovery, and toward
	// the owner when the requester cancels or exhausts its explicit local limit.
	// The requester cancels an entered phase and accepts late evidence only for
	// truthful accounting; it cannot reopen the request or renew permission.
	ProviderRecoveryStopped struct {
		// Err is the winning provider, deadline, cancellation, or control failure.
		// It must be non-nil.
		Err error
	}
)

func (ProviderWaitRequest) providerRecoveryMessage()            {}
func (ProviderAttemptRequest) providerRecoveryMessage()         {}
func (ProviderRecoveryPermission) providerRecoveryMessage()     {}
func (ProviderWaitObserved) providerRecoveryMessage()           {}
func (ProviderWaitFinished) providerRecoveryMessage()           {}
func (ProviderWaitObservationRequest) providerRecoveryMessage() {}
func (ProviderAttemptSucceeded) providerRecoveryMessage()       {}
func (ProviderAttemptFailed) providerRecoveryMessage()          {}
func (ProviderPhaseUnused) providerRecoveryMessage()            {}
func (ProviderPhaseFailed) providerRecoveryMessage()            {}
func (ProviderRecoverySettled) providerRecoveryMessage()        {}
func (ProviderRecoveryStopped) providerRecoveryMessage()        {}
