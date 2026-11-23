package sg

import (
	"net/http"
	"time"
)

// FakeOVN exposes the fake h3ovn API to the provider-level tests (sg_test).
type FakeOVN struct{ f *fakeOVN }

func NewFakeOVN() *FakeOVN               { return &FakeOVN{f: newFakeOVN()} }
func (x *FakeOVN) Handler() http.Handler { return x.f }
func (x *FakeOVN) VMGroups(vm string) []string {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	return append([]string{}, x.f.vmGroups[vm]...)
}
func (x *FakeOVN) DefaultID() string {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	for _, g := range x.f.groups {
		if g.IsDefault {
			return g.ID
		}
	}
	return ""
}
func (x *FakeOVN) Groups() map[string]Group {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	out := map[string]Group{}
	for id, g := range x.f.groups {
		out[id] = x.f.view(g)
	}
	return out
}
func (x *FakeOVN) Calls() []string {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	return append([]string{}, x.f.calls...)
}

const TestProject, TestVM = project, vmID

// SetPointAPI switches the point attach/detach routes on or off.
func (x *FakeOVN) SetPointAPI(on bool) {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	x.f.noPoint = !on
}

// PointConflicts answers the next n point calls with 409 REVISION_CONFLICT.
func (x *FakeOVN) PointConflicts(n int) {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	x.f.pointConflict = n
}

// FastPointRetry shortens the wait between REVISION_CONFLICT retries.
func FastPointRetry() { pointRetry.wait = time.Millisecond }
