package terminal

import (
	"fmt"
	"runtime"

	"github.com/safing/portbase/container"
	"github.com/safing/spn/unit"
)

var scheduler *unit.Scheduler

// Msg is a message within the SPN network stack.
// It includes metadata and unit scheduling.
type Msg struct {
	FlowID uint32
	Type   MsgType
	Data   *container.Container

	// Unit scheduling.
	// Note: With just 100B per packet, a uint64 (the Unit ID) is enough for
	// over 1800 Exabyte. No need for overflow support.
	*unit.Unit
}

// NewMsg returns a new msg.
// The FlowID is unset.
// The Type is Data.
func NewMsg(data []byte) *Msg {
	msg := &Msg{
		Type: MsgTypeData,
		Data: container.New(data),
		Unit: scheduler.NewUnit(),
	}

	// Debug unit leaks.
	// msg.Debug()

	return msg
}

// NewEmptyMsg returns a new empty msg with an initialized Unit.
// The FlowID is unset.
// The Type is Data.
// The Data is unset.
func NewEmptyMsg() *Msg {
	msg := &Msg{
		Type: MsgTypeData,
		Unit: scheduler.NewUnit(),
	}

	// Debug unit leaks.
	// msg.Debug()

	return msg
}

// Pack prepends the message header (Length and ID+Type) to the data.
func (msg *Msg) Pack() {
	MakeMsg(msg.Data, msg.FlowID, msg.Type)
}

// Consume adds another Message to itself.
// The given Msg is packed before adding it to the data.
// The data is moved - not copied!
// High priority mark is inherited.
func (msg *Msg) Consume(other *Msg) {
	// Pack message to be added.
	other.Pack()

	// Move data.
	msg.Data.AppendContainer(other.Data)

	// Inherit high priority.
	if other.IsHighPriorityUnit() {
		msg.MakeUnitHighPriority()
	}

	// Finish other unit.
	other.FinishUnit()
}

// FinishUnit signals the unit scheduler that this unit has finished processing.
// Will no-op if called on a nil Msg.
func (msg *Msg) FinishUnit() {
	// Proxying is necessary, as a nil msg still panics.
	if msg == nil {
		return
	}
	msg.Unit.FinishUnit()
}

// DebugUnit registers the given unit with the given source for debug output.
// Additional calls on the same unit update the unit source.

// Debug registers the unit for debug output with the given source.
// Additional calls on the same unit update the unit source.
// StartDebugLog() must be called before calling DebugUnit().
func (msg *Msg) Debug() {
	if msg == nil {
		return
	}
	_, file, line, ok := runtime.Caller(1)
	if ok {
		scheduler.DebugUnit(msg.Unit, fmt.Sprintf("%s:%d", file, line))
	}
}
