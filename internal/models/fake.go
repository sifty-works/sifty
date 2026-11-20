package models

import (
	"context"
	"sync"
)

type FakeModel struct {
	mu      sync.Mutex
	calls   []Request
	replies []Response
	errors  []error
	idx     int
}

func (f *FakeModel) AddReply(r Response) {
	f.mu.Lock()
	f.replies = append(f.replies, r)
	f.errors = append(f.errors, nil)
	f.mu.Unlock()
}
func (f *FakeModel) AddError(e error) {
	f.mu.Lock()
	f.replies = append(f.replies, Response{})
	f.errors = append(f.errors, e)
	f.mu.Unlock()
}
func (f *FakeModel) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

func (f *FakeModel) Complete(_ context.Context, r Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	if f.idx >= len(f.replies) {
		return Response{Text: "ok"}, nil
	}
	resp, err := f.replies[f.idx], f.errors[f.idx]
	f.idx++
	return resp, err
}
